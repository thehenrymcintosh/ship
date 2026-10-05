package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/brief"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
	"github.com/thehenrymcintosh/ship/internal/tmpl"
)

// Agent executes agent steps.
type Agent struct{}

// Execute runs the agent and validates its structured result.
func (Agent) Execute(ctx context.Context, v *Visit) (Result, error) {
	outcomes := v.Step.OutcomeNames()
	save := v.Step.Save.VarNames()
	call, fail := buildCall(v, outcomes, "")
	if fail != nil {
		return *fail, nil
	}
	call.schema = agent.OutcomeSchema(outcomes, save)
	call.preamble.Save = save
	out, r := invoke(ctx, v, call)
	if r.Outcome != "" {
		return r, nil
	}
	r.Outcome, r.Summary = out.Outcome, out.Summary
	if len(save) > 0 {
		r.Vars = map[string]string{}
		for _, name := range save {
			r.Vars[name] = out.Vars[name]
		}
	}
	return r, nil
}

// agentCall is one prepared invocation.
type agentCall struct {
	adapter  agent.Adapter
	cfg      pipeline.AgentConfig
	prompt   string
	preamble agent.Preamble
	schema   json.RawMessage
	// check adds semantic validation beyond the schema.
	check func(out agent.Output) error
}

// buildCall resolves the adapter, prompt and preamble for a visit.
func buildCall(v *Visit, outcomes []string, extra string) (*agentCall, *Result) {
	cfg := v.Pipeline.AgentFor(v.RT.AgentBase(), v.Step)
	if v.ForceCLI != "" {
		cfg.CLI = v.ForceCLI
	}
	if cfg.CLI == "" {
		cfg.CLI = "claude"
	}
	ad, err := v.RT.Agents().Get(cfg.CLI)
	if err != nil {
		r := ErrorResult("agent_cli", err.Error())
		return nil, &r
	}
	c := &agentCall{adapter: ad, cfg: cfg}

	// task renders the step's own prompt (skill line, then prompt text).
	task := func() (string, *Result) {
		var parts []string
		line := pipeline.Str(v.Step.Agent)
		if v.Step.Split != nil {
			line = pipeline.Str(v.Step.Split)
		}
		if line != "" {
			r, err := tmpl.Render(line, v.Scope, tmpl.Plain)
			if err != nil {
				res := renderError(err)
				return "", &res
			}
			parts = append(parts, r)
		}
		if v.Step.Prompt != nil {
			r, err := tmpl.Render(*v.Step.Prompt, v.Scope, tmpl.Plain)
			if err != nil {
				res := renderError(err)
				return "", &res
			}
			parts = append(parts, r)
		}
		p := strings.Join(parts, "\n\n")
		if strings.TrimSpace(p) == "" {
			// e.g. `split: ""` with no prompt: the system prompt says what to do.
			p = "Do the work for this step as described in your instructions, then report your result."
		}
		return p, nil
	}

	switch v.ResumeKind {
	case "interrupted":
		c.prompt = agent.ResumePrompt
	case "continue":
		c.prompt = agent.RevisitPrompt(v.StepName, v.Number, v.Since)
	case "shared":
		t, fail := task()
		if fail != nil {
			return nil, fail
		}
		c.prompt = agent.SharedPrompt(v.StepName, v.Number, v.Since, t)
	case "resplit":
		c.prompt = "The reviewer asked for a different split:\n\n" + v.Note + "\n\nRe-split the brief taking this into account, then report your result again."
	default:
		t, fail := task()
		if fail != nil {
			return nil, fail
		}
		c.prompt = t
		if v.ResumeKind == "resplit-fresh" {
			c.prompt += "\n\nThe reviewer asked for a different split:\n\n" + v.Note
		}
	}

	var infos []agent.OutcomeInfo
	human := ""
	for _, o := range outcomes {
		t, _ := v.Step.Target(o)
		infos = append(infos, agent.OutcomeInfo{Name: o, Target: describeTarget(v, t)})
		if human == "" {
			if ts, ok := v.Pipeline.Steps[t]; ok && ts.Type() == pipeline.TypeAsk {
				human = o
			}
		}
	}
	rules := ""
	if v.Step.Rules != "" {
		rules = v.Step.Rules
	}
	c.preamble = agent.Preamble{
		Pipeline: v.PipelineName, RunID: v.RunID, Step: v.StepName, VisitNumber: v.Number,
		Worktree: v.Worktree, Branch: v.Snapshot.Branch, BriefPath: v.BriefPath, Acceptance: v.Acceptance,
		Prev: v.Prev, RunNotes: v.RunNotes, Context: v.Step.Context, Rules: rules, Extra: extra,
		Outcomes: infos, HumanOutcome: human,
	}
	return c, nil
}

func describeTarget(v *Visit, t string) string {
	switch t {
	case pipeline.TargetDone:
		return "finish successfully"
	case pipeline.TargetStop:
		return "stop the run"
	case pipeline.TargetCameFrom:
		if v.CameFrom != "" {
			return fmt.Sprintf("back to %q", v.CameFrom)
		}
		return "back to the previous step"
	}
	s, ok := v.Pipeline.Steps[t]
	if !ok {
		return t
	}
	if s.Type() == pipeline.TypeAsk {
		return "a human check-in"
	}
	if s.Description != "" {
		return fmt.Sprintf("%s (%s)", firstLine(s.Description), t)
	}
	return fmt.Sprintf("%s (%s)", t, s.Type())
}

// invoke runs the adapter, with one corrective retry for invalid output.
// When the returned Result has an Outcome, the visit is over (error or
// cancelled); otherwise out holds the valid output and Result carries cost
// and session bookkeeping.
func invoke(ctx context.Context, v *Visit, c *agentCall) (agent.Output, Result) {
	var r Result
	b, over := visitBudget(v)
	if over != nil {
		return agent.Output{}, *over
	}
	preamble := c.preamble.Render()
	_ = writeFile(v.File("input.md"), "## Prompt\n\n"+c.prompt+"\n\n## Preamble\n\n"+preamble+"\n## Output schema\n\n```json\n"+string(c.schema)+"\n```\n")

	tw, err := openLog(v.File("transcript.jsonl"), true)
	if err != nil {
		return agent.Output{}, ErrorResult("start", err.Error())
	}
	defer tw.Close()
	ew, err := openLog(v.File("stderr.log"), true)
	if err != nil {
		return agent.Output{}, ErrorResult("start", err.Error())
	}
	defer ew.Close()

	req := agent.Request{
		Workdir: v.Worktree, Prompt: c.prompt, SystemAppend: preamble,
		Model: c.cfg.Model, Effort: c.cfg.Effort,
		Permission:   agent.Permission{Mode: c.cfg.PermissionMode, Allowed: c.cfg.AllowedTools, Disallowed: c.cfg.DisallowedTools},
		ReadDirs:     []string{v.RunDir},
		OutputSchema: c.schema, SessionID: v.SessionID, ResumeID: v.ResumeID,
		Timeout: v.Timeout, Env: v.Env, ExtraArgs: c.cfg.ExtraArgs,
		TranscriptW: tw, StderrW: io.MultiWriter(ew, outputWriter{v, "stderr"}),
		RunID: v.RunID, Step: v.StepName, VisitNumber: v.Number,
	}
	sink := agent.SinkFunc(func(e agent.UIEvent) { v.RT.AgentEvent(e) })
	fail := func(e Result) (agent.Output, Result) {
		e.Cost, e.Tokens, e.TokenUsage, e.SessionID, e.PermissionDenials, e.Usage = r.Cost, r.Tokens, r.TokenUsage, r.SessionID, r.PermissionDenials, r.Usage
		return agent.Output{}, e
	}

	for attempt := 0; attempt < 2; {
		req.Attempt = attempt
		// What's left of the budgets after earlier tries in this visit.
		req.BudgetUSD, req.MaxTokens = 0, 0
		if b.usd > 0 {
			req.BudgetUSD = max(b.usd-r.Cost, 0.0001)
		}
		if b.tokens > 0 {
			req.MaxTokens = max(b.tokens-r.Tokens, 1)
		}
		resp, err := c.adapter.Run(ctx, req, sink)
		r.Cost += resp.CostUSD
		r.Tokens += resp.Tokens
		u := resp.TokenUsage
		r.TokenUsage.Add(store.TokenUsage{Input: u.Input, Output: u.Output, CacheWrite: u.CacheCreation, CacheRead: u.CacheRead})
		r.PermissionDenials = append(r.PermissionDenials, resp.PermissionDenials...)
		if resp.SessionID != "" {
			r.SessionID = resp.SessionID
		}
		if len(resp.Usage) > 0 {
			r.Usage = resp.Usage
		}
		if ctx.Err() != nil {
			return agent.Output{}, Result{Outcome: OutcomeCancelled, Summary: "cancelled", Cost: r.Cost, Tokens: r.Tokens, TokenUsage: r.TokenUsage, SessionID: r.SessionID}
		}
		// A limit only counts when the invocation failed, and not at a
		// budget: the CLI can report a spent window and carry on (on extra
		// usage, say).
		if resp.Limited && (err != nil || resp.IsError) && resp.OverBudget == "" {
			// Wait the limit out, then carry on in the same conversation.
			wait, ok := limitWait(resp.RetryAt, req.LimitRetry)
			if !ok {
				return fail(ErrorResult("rate_limited", "Claude kept refusing for a usage or rate limit: "+resp.ErrorText))
			}
			until := time.Now().Add(wait)
			note := fmt.Sprintf("Claude's usage limit was hit; carrying on at %s", until.Format("15:04"))
			v.RT.AgentEvent(agent.UIEvent{Kind: "system", Data: map[string]any{"text": note}})
			_ = v.RT.SetStatus(store.StatusWaiting, note)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return agent.Output{}, Result{Outcome: OutcomeCancelled, Summary: "cancelled", Cost: r.Cost, Tokens: r.Tokens, TokenUsage: r.TokenUsage, SessionID: r.SessionID}
			}
			_ = v.RT.SetStatus(store.StatusRunning, "")
			req.LimitRetry++
			if sid := firstNonEmpty(resp.SessionID, req.ResumeID, req.SessionID); sid != "" {
				req.ResumeID, req.SessionID = sid, ""
				req.Prompt = agent.LimitResumePrompt
			}
			continue
		}
		if resp.OverBudget != "" {
			reason := "budget"
			if (resp.OverBudget == "usd" && b.runUSD) || (resp.OverBudget == "tokens" && b.runTokens) {
				reason = "run_budget"
			}
			msg := "the agent stopped at its budget: " + resp.ErrorText
			if reason == "run_budget" {
				msg = "the run reached its budget: " + resp.ErrorText
			}
			if resp.OverBudget == "tokens" && resp.CostUSD == 0 && resp.Tokens > 0 {
				// Stopping claude loses the result line that carries the cost.
				est := float64(resp.Tokens) * costPerToken(v.Snapshot)
				r.Cost += est
				msg += fmt.Sprintf(" (cost estimated at $%.2f from this run's cost per token so far, as claude was stopped before reporting it)", est)
			}
			return fail(ErrorResult(reason, msg))
		}
		if err != nil || resp.IsError {
			msg := resp.ErrorText
			if err != nil {
				msg = err.Error()
			}
			reason := "agent_error"
			if strings.HasPrefix(msg, "timed out") {
				reason = "timeout"
			}
			if len(resp.PermissionDenials) > 0 {
				msg += fmt.Sprintf(" (%d permission denials)", len(resp.PermissionDenials))
			}
			return fail(ErrorResult(reason, msg))
		}
		verr := agent.ValidateOutput(c.schema, resp.Structured)
		var out agent.Output
		if verr == nil {
			if err := json.Unmarshal(resp.Structured, &out); err != nil {
				verr = err
			} else if c.check != nil {
				verr = c.check(out)
			}
		}
		if verr == nil {
			return out, r
		}
		if attempt == 0 {
			v.RT.AgentEvent(agent.UIEvent{Kind: "system", Data: map[string]any{"text": "invalid structured result, asking for a correction: " + verr.Error()}})
			req.ResumeID = firstNonEmpty(resp.SessionID, req.ResumeID, req.SessionID)
			req.SessionID = ""
			req.Prompt = agent.CorrectionPrompt(verr.Error())
			attempt++
			continue
		}
		return fail(ErrorResult("invalid_output", "the agent's structured result was invalid after a correction: "+verr.Error()))
	}
	panic("unreachable")
}

// Limit waits: until the reset Claude gives plus a margin, or, when it gives
// none (rate limits, overload), backing off from LimitBackoff up to 30m.
var (
	LimitMargin  = 30 * time.Second
	LimitBackoff = time.Minute
)

// limitRetries caps retries when Claude gives no reset time.
const limitRetries = 8

func limitWait(retryAt time.Time, retry int) (time.Duration, bool) {
	if !retryAt.IsZero() {
		return max(time.Until(retryAt), 0) + LimitMargin, true
	}
	if retry >= limitRetries {
		return 0, false
	}
	return min(LimitBackoff<<retry, 30*time.Minute), true
}

// budget is what one visit may spend: the tighter of the step's own limit
// and what's left of the run's (0 = no limit).
type budget struct {
	usd               float64
	tokens            int64
	runUSD, runTokens bool // the run's limit is the binding one
}

func visitBudget(v *Visit) (budget, *Result) {
	var b budget
	s := v.Snapshot
	if limit := v.Pipeline.MaxBudget(); limit > 0 {
		limit += s.ExtraBudgetUSD
		if s.CostUSD >= limit {
			e := ErrorResult("run_budget", fmt.Sprintf("the run has spent $%.2f of its $%.2f budget", s.CostUSD, limit))
			return b, &e
		}
		b.usd, b.runUSD = limit-s.CostUSD, true
	}
	if limit := v.Pipeline.MaxTokens(); limit > 0 {
		limit += s.ExtraTokens
		if s.Tokens >= limit {
			e := ErrorResult("run_budget", fmt.Sprintf("the run has used %s of its %s tokens", FormatTokens(s.Tokens), FormatTokens(limit)))
			return b, &e
		}
		b.tokens, b.runTokens = limit-s.Tokens, true
	}
	if st := v.Step.MaxBudgetUSD; st != nil && *st > 0 && (b.usd == 0 || *st < b.usd) {
		b.usd, b.runUSD = *st, false
	}
	if st := v.Step.MaxTokens.N(); st > 0 && (b.tokens == 0 || st < b.tokens) {
		b.tokens, b.runTokens = st, false
	}
	return b, nil
}

// FormatTokens writes a token count briefly: 950, 12.3k, 1.2m.
func FormatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + "m"
	case n >= 1000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e3), ".0") + "k"
	}
	return fmt.Sprint(n)
}

// FormatUsage writes a visit's tokens briefly, with the breakdown when
// there is one: "41k tok · in 12k / out 9k / cache 20k write, 300k read".
func FormatUsage(tokens int64, u *store.TokenUsage) string {
	if u == nil || u.IsZero() {
		if tokens <= 0 {
			return ""
		}
		return FormatTokens(tokens) + " tok"
	}
	if tokens <= 0 {
		tokens = u.Total()
	}
	return fmt.Sprintf("%s tok · in %s / out %s / cache %s write, %s read", FormatTokens(tokens),
		FormatTokens(u.Input), FormatTokens(u.Output), FormatTokens(u.CacheWrite), FormatTokens(u.CacheRead))
}

// DescribeUsage spells a visit's tokens out in full, for a tooltip.
func DescribeUsage(tokens int64, u *store.TokenUsage) string {
	if u == nil || u.IsZero() {
		if tokens <= 0 {
			return ""
		}
		return fmt.Sprintf("%d tokens (input, cache writes and output)", tokens)
	}
	if tokens <= 0 {
		tokens = u.Total()
	}
	return fmt.Sprintf("%d tokens counted toward budgets\ninput: %d\noutput: %d\ncache writes: %d\ncache reads: %d (re-reading the conversation, not counted)",
		tokens, u.Input, u.Output, u.CacheWrite, u.CacheRead)
}

type outputWriter struct {
	v      *Visit
	stream string
}

func (w outputWriter) Write(p []byte) (int, error) {
	w.v.RT.Output(w.stream, append([]byte(nil), p...))
	return len(p), nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// costPerToken is what the run's finished agent visits cost per token, or 0
// before any has reported both.
func costPerToken(s *store.RunSnapshot) float64 {
	if s == nil {
		return 0
	}
	var usd float64
	var tokens int64
	for _, vs := range s.Visits {
		if vs.Finished != nil && (vs.Type == pipeline.TypeAgent || vs.Type == pipeline.TypeSplit) && vs.CostUSD > 0 && vs.Tokens > 0 {
			usd += vs.CostUSD
			tokens += vs.Tokens
		}
	}
	if tokens == 0 {
		return 0
	}
	return usd / float64(tokens)
}

// --- split ------------------------------------------------------------------

// Split executes split steps.
type Split struct{}

// SummaryFile keeps the split agent's summary for a resumed review.
const SummaryFile = "split-summary.md"

// Execute runs the split agent, writes slice briefs, and (with review) waits
// for a human to approve them.
func (Split) Execute(ctx context.Context, v *Visit) (Result, error) {
	if v.Resumed {
		summary, _ := os.ReadFile(v.File(SummaryFile))
		return reviewSplit(ctx, v, strings.TrimSpace(string(summary)), v.Snapshot.ProposedSlices, 0, nil)
	}
	outcomes := v.Step.OutcomeNames()
	max := v.Step.MaxSlicesN()
	extra := fmt.Sprintf(`Your job in this step is to split the brief into slices: at most %d, each self-contained and independently reviewable, listed in the order they should land.
Each slice needs a key (lowercase letters, digits and dashes, e.g. "schema"), a title, a self-contained markdown brief, and acceptance criteria.
If the work fits in one change, return a single slice covering everything. If you can't split it, choose an outcome other than "ok".`, max)
	vars := sliceVars(v)
	if len(vars) > 0 {
		var lines []string
		for _, n := range sortedNames(vars) {
			line := "- " + n
			if vars[n] != "" {
				line += ": " + vars[n]
			}
			lines = append(lines, line)
		}
		extra += "\n\nEach slice runs with its own copy of these variables, which default to this run's values. When a slice needs a different value, set it in the slice's vars: for example, when the brief covers several tickets, give each slice the ticket it delivers.\n" + strings.Join(lines, "\n")
	}
	if v.ResumeKind == "resplit" || v.ResumeKind == "resplit-fresh" {
		extra += "\n\nA human reviewed your previous split and asked for changes:\n" + v.Note
	}
	call, fail := buildCall(v, outcomes, extra)
	if fail != nil {
		return *fail, nil
	}
	call.schema = agent.SplitSchema(outcomes, max, sortedNames(vars))
	call.preamble.Slices = true
	call.check = func(out agent.Output) error {
		if out.Outcome != "ok" {
			return nil
		}
		if len(out.Slices) == 0 {
			return errors.New(`outcome "ok" needs at least one slice`)
		}
		seen := map[string]bool{}
		for _, s := range out.Slices {
			if seen[s.Key] {
				return fmt.Errorf("duplicate slice key %q", s.Key)
			}
			seen[s.Key] = true
		}
		return nil
	}
	out, r := invoke(ctx, v, call)
	if r.Outcome != "" {
		return r, nil
	}
	if out.Outcome != "ok" {
		r.Outcome, r.Summary = out.Outcome, out.Summary
		return r, nil
	}
	_ = writeFile(v.File(SummaryFile), out.Summary)
	slices, err := writeSlices(v, out.Slices)
	if err != nil {
		e := ErrorResult("slices", err.Error())
		e.Cost, e.Tokens, e.TokenUsage, e.SessionID = r.Cost, r.Tokens, r.TokenUsage, r.SessionID
		return e, nil
	}
	if err := v.RT.Emit(store.EvSlicesProposed, store.SlicesProposed{Seq: v.Seq, Slices: slices}); err != nil {
		return Result{}, err
	}
	return reviewSplit(ctx, v, out.Summary, slices, r.Cost, &r)
}

// sliceVars returns the variables a slice can set (name → description):
// those the pipelines this run fans out to take from their brief or parent.
func sliceVars(v *Visit) map[string]string {
	out := map[string]string{}
	if v.Pipeline == nil {
		return out
	}
	l := pipeline.NewLoader(filepath.Join(v.RunDir, store.PipelineDir))
	for _, st := range v.Pipeline.Steps {
		if st == nil || st.Fanout == nil {
			continue
		}
		f, _ := l.Load(*st.Fanout)
		if f == nil {
			continue
		}
		for name, vr := range f.Pipeline.Variables {
			if vr == nil {
				continue
			}
			if src := vr.Source(); src == pipeline.SourceFromBrief || src == pipeline.SourceFromParent {
				out[name] = vr.Description
			}
		}
	}
	return out
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeSlices(v *Visit, specs []agent.SliceOut) ([]store.Slice, error) {
	dir := v.File("slices")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var out []store.Slice
	for i, s := range specs {
		name := brief.SliceFileName(i+1, s.Key)
		data := brief.RenderSlice(brief.SliceSpec{Key: s.Key, Title: s.Title, Brief: s.Brief, Acceptance: s.Acceptance, Vars: s.Vars}, i+1, len(specs), v.RunID, v.BriefPath)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
		out = append(out, store.Slice{Key: s.Key, Title: s.Title, File: path, Number: i + 1})
	}
	return out, nil
}

// ReloadSlices re-reads the slice files after a human edited them.
func ReloadSlices(dir string) ([]store.Slice, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	sort.Strings(files)
	if len(files) == 0 {
		return nil, errors.New("no slice files left in " + dir)
	}
	var out []store.Slice
	seen := map[string]bool{}
	for i, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		b, err := brief.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", filepath.Base(f), err)
		}
		key := ""
		if b.Slice != nil {
			key = b.Slice.Key
		}
		if key == "" {
			// Derive from the file name: NN-<key>.md
			base := strings.TrimSuffix(filepath.Base(f), ".md")
			if _, rest, ok := strings.Cut(base, "-"); ok {
				key = rest
			} else {
				key = base
			}
		}
		if seen[key] {
			return nil, fmt.Errorf("%s: duplicate slice key %q", filepath.Base(f), key)
		}
		seen[key] = true
		out = append(out, store.Slice{Key: key, Title: b.Title, File: f, Number: i + 1})
	}
	return out, nil
}

func reviewSplit(ctx context.Context, v *Visit, summary string, slices []store.Slice, cost float64, base *Result) (Result, error) {
	ok := Result{Outcome: "ok", Summary: summary, Cost: cost}
	if base != nil {
		ok.SessionID, ok.PermissionDenials, ok.Usage = base.SessionID, base.PermissionDenials, base.Usage
		ok.Tokens, ok.TokenUsage = base.Tokens, base.TokenUsage
	}
	sliceSummary := func(sl []store.Slice) string {
		var b strings.Builder
		b.WriteString(summary)
		b.WriteString("\n\nSlices:\n")
		for _, s := range sl {
			fmt.Fprintf(&b, "%d. %s (`%s`)\n", s.Number, s.Title, s.Key)
		}
		return strings.TrimSpace(b.String())
	}
	v.RT.Idle()
	if !v.Step.Review {
		if err := v.RT.Emit(store.EvSlicesApproved, store.SeqOnly{Seq: v.Seq}); err != nil {
			return Result{}, err
		}
		ok.Summary = sliceSummary(slices)
		return ok, nil
	}
	if !v.Resumed {
		q := fmt.Sprintf("Review the %d proposed slices for %q", len(slices), v.Snapshot.Title)
		if err := v.RT.Emit(store.EvAskPending, store.AskPending{
			Seq: v.Seq, Kind: store.AskKindSplitReview, Question: q,
			Choices: []string{"approve", "resplit", "reload", "stop"}, Input: "optional", Show: []string{"slices"},
		}); err != nil {
			return Result{}, err
		}
		v.RT.Notify(v.Snapshot.Title, q)
	}
	if err := v.RT.SetStatus(store.StatusAsking, "split review"); err != nil {
		return Result{}, err
	}
	for {
		cmd, err := v.RT.Await(ctx)
		if err != nil {
			return Result{Outcome: OutcomeCancelled, Summary: "cancelled", Cost: cost, Tokens: ok.Tokens, TokenUsage: ok.TokenUsage}, nil
		}
		action := cmd.Action
		if cmd.Name == "answer" && action == "" {
			action = cmd.Choice
		}
		answered := func() error {
			return v.RT.Emit(store.EvAskAnswered, store.AskAnswered{Seq: v.Seq, Choice: action, Note: cmd.Note})
		}
		switch action {
		case "approve":
			if err := answered(); err != nil {
				cmd.Respond(err)
				return Result{}, err
			}
			if err := v.RT.Emit(store.EvSlicesApproved, store.SeqOnly{Seq: v.Seq}); err != nil {
				cmd.Respond(err)
				return Result{}, err
			}
			cmd.Respond(nil)
			ok.Summary = sliceSummary(slices)
			if strings.TrimSpace(cmd.Note) != "" {
				ok.Summary += "\n\nReviewer: " + cmd.Note
			}
			ok.HumanReset = true
			return ok, nil
		case "resplit":
			if strings.TrimSpace(cmd.Note) == "" {
				cmd.Respond(&InvalidError{"re-split needs a note saying what to change"})
				continue
			}
			if err := answered(); err != nil {
				cmd.Respond(err)
				return Result{}, err
			}
			cmd.Respond(nil)
			return Result{Outcome: "resplit", Internal: "resplit", Summary: cmd.Note, Cost: cost, Tokens: ok.Tokens, TokenUsage: ok.TokenUsage, SessionID: ok.SessionID, HumanReset: true}, nil
		case "reload":
			fresh, err := ReloadSlices(v.File("slices"))
			if err != nil {
				cmd.Respond(&InvalidError{err.Error()})
				continue
			}
			if err := v.RT.Emit(store.EvSlicesProposed, store.SlicesProposed{Seq: v.Seq, Slices: fresh}); err != nil {
				cmd.Respond(err)
				return Result{}, err
			}
			slices = fresh
			cmd.Respond(nil)
		case "stop":
			if err := answered(); err != nil {
				cmd.Respond(err)
				return Result{}, err
			}
			cmd.Respond(nil)
			s := "Stopped at split review"
			if strings.TrimSpace(cmd.Note) != "" {
				s += ": " + cmd.Note
			}
			return Result{Outcome: "stop", Internal: "stop", Summary: s, Cost: cost, Tokens: ok.Tokens, TokenUsage: ok.TokenUsage, SessionID: ok.SessionID, HumanReset: true}, nil
		default:
			cmd.Respond(&InvalidError{fmt.Sprintf("unknown review action %q (approve, resplit, reload, stop)", action)})
		}
	}
}
