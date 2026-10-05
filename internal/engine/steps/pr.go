package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/ghpr"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
	"github.com/thehenrymcintosh/ship/internal/tmpl"
)

// AgentReplyMarker marks PR comments an agent posted, so they aren't fed
// back as new review feedback.
const AgentReplyMarker = "<!-- ship-agent -->"

// PRTrigger is the command that sends pending feedback now.
const PRTrigger = "pr_trigger"

// rerun is a re-run ship requested for a cancelled check.
type rerun struct {
	URL string    `json:"url"` // the cancelled run's link
	At  time.Time `json:"at"`
}

// rerunGrace is how long a re-run cancelled check may keep showing its old
// result before it counts as a failure (GitHub's rollup can lag).
const rerunGrace = 10 * time.Minute

// prState is what a pr step has already acted on, kept in the run dir so it
// survives restarts and revisits.
type prState struct {
	Seen     map[string]bool  `json:"seen"`     // comment ids already sent on
	CIFired  map[string]bool  `json:"ci_fired"` // head SHAs whose CI failure was sent on
	Ready    map[string]bool  `json:"ready"`    // head SHAs whose readiness was sent on
	Reruns   map[string]rerun `json:"reruns"`   // head/check → the re-run requested for it
	Triggers int              `json:"triggers"` // manual triggers pending
}

func prStatePath(v *Visit) string {
	return filepath.Join(v.RunDir, "pr-state-"+v.StepName+".json")
}

func loadPRState(v *Visit) *prState {
	st := &prState{Seen: map[string]bool{}, CIFired: map[string]bool{}, Ready: map[string]bool{}, Reruns: map[string]rerun{}}
	if b, err := os.ReadFile(prStatePath(v)); err == nil {
		json.Unmarshal(b, st)
	}
	for _, m := range []*map[string]bool{&st.Seen, &st.CIFired, &st.Ready} {
		if *m == nil {
			*m = map[string]bool{}
		}
	}
	if st.Reruns == nil {
		st.Reruns = map[string]rerun{}
	}
	return st
}

func (st *prState) save(v *Visit) {
	b, _ := json.MarshalIndent(st, "", "  ")
	store.WriteFileAtomic(prStatePath(v), b, 0o600)
}

// PR executes `pr:` steps: it watches a pull request without an agent and
// routes on what happens to it.
type PR struct {
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

func (p PR) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// isFeedback reports whether a comment is review feedback for the agents:
// not empty, not a "ship:" note about the pipeline, not an agent's own reply.
func isFeedback(c ghpr.Comment) bool {
	body := strings.TrimSpace(c.Body)
	if strings.Contains(body, AgentReplyMarker) {
		return false
	}
	if strings.HasPrefix(strings.ToLower(body), "ship:") {
		return false
	}
	return body != "" || c.State == "CHANGES_REQUESTED"
}

// Execute polls until the PR does something the pipeline routes on.
func (p PR) Execute(ctx context.Context, v *Visit) (Result, error) {
	if err := v.RT.SetStatus(store.StatusWaiting, "watching the PR"); err != nil {
		return Result{}, err
	}
	ref, err := tmpl.Render(pipeline.Str(v.Step.PR), v.Scope, tmpl.Plain)
	if err != nil {
		return renderError(err), nil
	}
	if strings.TrimSpace(ref) == "" {
		ref, _ = v.Scope.Value("run.branch")
	}
	mapped := func(o string) bool {
		t, ok := v.Step.Target(o)
		return ok && t != ""
	}
	st := loadPRState(v)
	started := v.StartedAt
	if started.IsZero() {
		started = p.now()
	}
	deadline := started.Add(v.Timeout)
	every := v.Step.EveryD()
	settle := v.Step.SettleD()
	trigger := v.Step.TriggerOrDefault()
	failures := 0
	var last store.PRStatus
	var log []string
	logf := func(format string, a ...any) {
		line := p.now().Format("15:04:05") + " " + fmt.Sprintf(format, a...)
		log = append(log, line)
		if len(log) > 200 {
			log = log[len(log)-200:]
		}
		v.RT.Output("stdout", []byte(line+"\n"))
		_ = writeFile(v.File("stdout.log"), strings.Join(log, "\n")+"\n")
	}
	_ = writeFile(v.File("input.md"), fmt.Sprintf("Watching PR %q every %s; feedback is sent %s.\n", ref, every, map[string]string{"auto": fmt.Sprintf("once nobody has commented for %s", settle), "manual": "when you trigger it"}[trigger]))

	for {
		if v.Timeout > 0 && !p.now().Before(deadline) {
			if mapped("timeout") {
				return Result{Outcome: "timeout", Summary: fmt.Sprintf("The PR didn't merge or close within %s.", v.Timeout), Output: log}, nil
			}
			r := ErrorResult("timeout", fmt.Sprintf("watched the PR for %s without an outcome", v.Timeout))
			r.Output = log
			return r, nil
		}

		pr, err := ghpr.View(ctx, v.Worktree, v.Env, ref)
		if ctx.Err() != nil {
			return Result{Outcome: OutcomeCancelled, Summary: "cancelled"}, nil
		}
		status := store.PRStatus{Step: v.StepName, Seq: v.Seq, Trigger: trigger, SettleSeconds: int(settle.Seconds())}
		switch {
		case errors.Is(err, ghpr.ErrNoPR):
			failures = 0
			status.Note = fmt.Sprintf("no pull request for %q yet", ref)
			p.publish(v, &last, status)
		case err != nil:
			failures++
			logf("couldn't read the PR (%d in a row): %v", failures, err)
			if failures >= MaxPollFailures {
				r := ErrorResult("pr_unreadable", fmt.Sprintf("couldn't read the PR %d times in a row; is gh installed and logged in? Last error: %v", failures, err))
				r.Output = log
				return r, nil
			}
		default:
			failures = 0
			res, done := p.decide(ctx, v, pr, st, &status, mapped, settle, trigger, logf)
			p.publish(v, &last, status)
			st.save(v)
			if done {
				res.Output = log
				return res, nil
			}
		}

		// Wait for the next poll, or a manual trigger.
		wctx, cancel := context.WithTimeout(ctx, every)
		cmd, err := v.RT.Await(wctx)
		cancel()
		if ctx.Err() != nil {
			return Result{Outcome: OutcomeCancelled, Summary: "cancelled"}, nil
		}
		if err == nil {
			if cmd.Name == PRTrigger {
				if !mapped("feedback") {
					cmd.Respond(&InvalidError{"this pipeline doesn't route PR feedback (map feedback: in the pr step's next)"})
					continue
				}
				st.Triggers++
				st.save(v)
				logf("feedback triggered by hand")
				cmd.Respond(nil)
			} else {
				cmd.Respond(&InvalidError{"the run is watching its PR; to send review comments now, use Address feedback"})
			}
		}
	}
}

func (p PR) publish(v *Visit, last *store.PRStatus, s store.PRStatus) {
	a, _ := json.Marshal(last)
	b, _ := json.Marshal(s)
	if string(a) != string(b) {
		*last = s
		v.RT.Emit(store.EvPRPolled, s)
	}
}

// decide looks at one observation. It returns a result and true when the
// pipeline should move on.
func (p PR) decide(ctx context.Context, v *Visit, pr *ghpr.PR, st *prState, status *store.PRStatus,
	mapped func(string) bool, settle time.Duration, trigger string, logf func(string, ...any)) (Result, bool) {

	status.URL, status.Number, status.State, status.HeadSHA, status.ReviewDecision = pr.URL, pr.Number, pr.State, pr.HeadSHA, pr.ReviewDecision

	switch pr.State {
	case "MERGED":
		if mapped("merged") {
			return Result{Outcome: "merged", Summary: fmt.Sprintf("PR #%d was merged: %s", pr.Number, pr.URL)}, true
		}
	case "CLOSED":
		if mapped("closed") {
			return Result{Outcome: "closed", Summary: fmt.Sprintf("PR #%d was closed without merging: %s", pr.Number, pr.URL)}, true
		}
	}

	// CI on the PR's current head.
	var failing []ghpr.Check
	for _, c := range pr.Checks {
		switch c.Bucket() {
		case ghpr.Pass, ghpr.Skipped:
			status.ChecksPass++
		case ghpr.Pending:
			status.ChecksPending++
		case ghpr.Cancelled:
			// A cancellation isn't a verdict: re-run it once before treating it
			// as a failure, and give the re-run time to show up.
			key := pr.HeadSHA + "\x00" + c.Label()
			prev, requested := st.Reruns[key]
			if requested && prev.URL == c.URL() && p.now().Sub(prev.At) < rerunGrace {
				status.ChecksPending++
				continue
			}
			if id := c.RunID(); id != "" && !requested {
				st.Reruns[key] = rerun{URL: c.URL(), At: p.now()}
				if err := ghpr.Rerun(ctx, v.Worktree, v.Env, id); err == nil {
					logf("check %q was cancelled; re-running it once", c.Label())
					status.ChecksPending++
					continue
				}
			}
			status.ChecksFail++
			failing = append(failing, c)
		case ghpr.Fail:
			status.ChecksFail++
			failing = append(failing, c)
		}
	}
	for _, c := range failing {
		status.Failing = append(status.Failing, c.Label())
	}
	settled := len(pr.Checks) > 0 && status.ChecksPending == 0
	if settled && len(failing) > 0 && pr.State == "OPEN" && !st.CIFired[pr.HeadSHA] && mapped("ci_failed") {
		st.CIFired[pr.HeadSHA] = true
		return Result{Outcome: "ci_failed", Summary: p.ciReport(ctx, v, pr, failing)}, true
	}

	// Review feedback not yet sent on.
	var pending []ghpr.Comment
	for _, c := range pr.Comments {
		if !st.Seen[c.ID] && isFeedback(c) {
			pending = append(pending, c)
		}
	}
	status.PendingComments = len(pending)
	if len(pending) > 0 {
		lastAt := pending[len(pending)-1].CreatedAt
		status.LastCommentAt = &lastAt
		quiet := p.now().Sub(lastAt) >= settle
		if mapped("feedback") && pr.State == "OPEN" && (st.Triggers > 0 || trigger == "auto" && quiet) {
			st.Triggers = 0
			for _, c := range pending {
				st.Seen[c.ID] = true
			}
			return Result{Outcome: "feedback", Summary: feedbackReport(pr, pending)}, true
		}
	}

	// Approved and green.
	if settled && len(failing) == 0 && pr.ReviewDecision == "APPROVED" && pr.State == "OPEN" && !st.Ready[pr.HeadSHA] && mapped("ready") {
		st.Ready[pr.HeadSHA] = true
		return Result{Outcome: "ready", Summary: fmt.Sprintf("PR #%d is approved and every check passed: %s", pr.Number, pr.URL)}, true
	}
	return Result{}, false
}

func (p PR) ciReport(ctx context.Context, v *Visit, pr *ghpr.PR, failing []ghpr.Check) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CI failed on PR #%d (%s) at %s.\n\n", pr.Number, pr.URL, short(pr.HeadSHA))
	logged := map[string]bool{}
	budget := 400
	for _, c := range failing {
		fmt.Fprintf(&b, "### %s: %s\n%s\n", c.Label(), strings.ToLower(firstNonEmpty(c.Conclusion, c.State)), c.URL())
		if id := c.RunID(); id != "" && !logged[id] && budget > 0 {
			logged[id] = true
			n := min(150, budget)
			if tail := ghpr.FailedLog(ctx, v.Worktree, v.Env, id, n); tail != "" {
				budget -= strings.Count(tail, "\n") + 1
				fmt.Fprintf(&b, "\nEnd of the failed log:\n```\n%s\n```\n", tail)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("Find the root cause and fix it. If the failure isn't caused by this change (flaky test, infrastructure), say so instead of changing code.")
	return b.String()
}

func feedbackReport(pr *ghpr.PR, comments []ghpr.Comment) string {
	var b strings.Builder
	people := map[string]bool{}
	for _, c := range comments {
		people[c.Author] = true
	}
	names := make([]string, 0, len(people))
	for n := range people {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(&b, "%d new piece(s) of review feedback on PR #%d (%s) from %s.\n\n", len(comments), pr.Number, pr.URL, strings.Join(names, ", "))
	for _, c := range comments {
		where := ""
		switch {
		case c.Path != "" && c.Line > 0:
			where = fmt.Sprintf(" on `%s:%d`", c.Path, c.Line)
		case c.Path != "":
			where = fmt.Sprintf(" on `%s`", c.Path)
		case c.Kind == "review":
			where = " (review: " + strings.ToLower(strings.ReplaceAll(c.State, "_", " ")) + ")"
		}
		fmt.Fprintf(&b, "- **%s**%s [link](%s):\n", c.Author, where, c.URL)
		body := strings.TrimSpace(c.Body)
		if body == "" {
			body = "(no comment)"
		}
		for _, line := range strings.Split(body, "\n") {
			b.WriteString("  > " + line + "\n")
		}
	}
	b.WriteString("\nAddress every point (or explain why not), keeping the reviewers' decisions. If you reply on the PR, include " + AgentReplyMarker + " in the reply.")
	return b.String()
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
