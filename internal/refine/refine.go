// Package refine turns accumulated feedback on a pipeline into a proposed
// new version (`ship pipeline refine`), and analyses feedback trends across
// versions (`ship pipeline report`). Claude does the reading and writing;
// people give the feedback and decide what to apply.
package refine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
)

// Context is everything a refinement or report needs.
type Context struct {
	Pipeline  string
	Repo      string // main checkout ("" for a global pipeline used outside a repo)
	Home      string // ~/.ship
	ClaudeDir string // ~/.claude
	Store     *history.Store
	Current   history.Version
	Agents    *agent.Registry
	CLI       string // agent adapter: "claude", or "fake" in tests
	Model     string
	Env       []string
	// RunsPerVersion counts runs of this pipeline by version, so trends can
	// be read relative to how much the pipeline was used.
	RunsPerVersion map[int]int
}

// Change replaces (or creates) one file.
type Change struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Why       string `json:"why"`
	Addresses []int  `json:"addresses"`
}

// Skipped is feedback the proposal deliberately doesn't act on.
type Skipped struct {
	ID  int    `json:"id"`
	Why string `json:"why"`
}

// Proposal is a proposed new version.
type Proposal struct {
	Summary   string    `json:"summary"`
	Changes   []Change  `json:"changes"`
	LeftAlone []Skipped `json:"left_alone"`

	Pipeline    string             `json:"pipeline"`
	FromVersion int                `json:"from_version"`
	FromHash    string             `json:"from_hash"`
	At          time.Time          `json:"at"`
	CostUSD     float64            `json:"cost_usd,omitempty"`
	Findings    []pipeline.Finding `json:"findings,omitempty"` // validation of the proposed pipeline files
}

// Addresses lists every feedback id the proposal's changes address.
func (p *Proposal) Addresses() []int {
	seen := map[int]bool{}
	var out []int
	for _, c := range p.Changes {
		for _, id := range c.Addresses {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Ints(out)
	return out
}

// file is one file shown to the model.
type file struct {
	Path    string // as the model must refer to it
	Abs     string
	Kind    string
	Content string
}

// display is how a path is shown to (and accepted from) the model:
// repo-relative inside the repo, absolute elsewhere.
func (c Context) display(abs string) string {
	if c.Repo != "" {
		if r, err := filepath.Rel(c.Repo, abs); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return abs
}

// resolve maps a path from the model back to an absolute path, and checks
// it's one the proposal may write: an existing input file, or a new file
// under .ship/ or a skills dir.
func (c Context) resolve(p string, inputs map[string]bool) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("empty path")
	}
	abs := p
	if !filepath.IsAbs(p) {
		if c.Repo == "" {
			return "", fmt.Errorf("%s: relative path outside a repo", p)
		}
		abs = filepath.Join(c.Repo, p)
	}
	abs = filepath.Clean(abs)
	if inputs[abs] {
		return abs, nil
	}
	var roots []string
	if c.Repo != "" {
		roots = append(roots, filepath.Join(c.Repo, brand.Dir), filepath.Join(c.Repo, ".claude", "skills"))
	}
	if c.Home != "" {
		roots = append(roots, filepath.Join(c.Home, "pipelines"), filepath.Join(c.Home, "bin"), filepath.Join(c.Home, "rules"))
	}
	if c.ClaudeDir != "" {
		roots = append(roots, filepath.Join(c.ClaudeDir, "skills"))
	}
	for _, r := range roots {
		if rel, err := filepath.Rel(r, abs); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			if strings.Contains(abs, string(filepath.Separator)+"history"+string(filepath.Separator)) {
				break // never rewrite history
			}
			return abs, nil
		}
	}
	return "", fmt.Errorf("%s isn't one of the pipeline's files or under %s/ or a skills dir", p, brand.Dir)
}

// inputs collects the current version's files.
func (c Context) inputs() []file {
	var out []file
	add := func(kind, abs string) {
		b, err := os.ReadFile(abs)
		if err != nil || len(b) > 256<<10 || !isText(b) {
			return
		}
		out = append(out, file{Path: c.display(abs), Abs: abs, Kind: kind, Content: string(b)})
	}
	for _, p := range c.Current.Parts {
		if p.Missing || p.Path == "" {
			continue
		}
		st, err := os.Stat(p.Path)
		if err != nil {
			continue
		}
		if st.IsDir() {
			_ = filepath.WalkDir(p.Path, func(f string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
					add(p.Kind, f)
				}
				return nil
			})
			continue
		}
		add(p.Kind, p.Path)
	}
	return out
}

func isText(b []byte) bool {
	for _, c := range b[:min(len(b), 8000)] {
		if c == 0 {
			return false
		}
	}
	return true
}

func feedbackLine(it history.Item) string {
	where := ""
	if it.Run != "" {
		where = " run " + it.Run
		if it.Step != "" {
			where += ", step " + it.Step
		}
	} else if it.Step != "" {
		where = " step " + it.Step
	}
	src := it.Source
	if it.Author != "" {
		src += " by " + it.Author
	}
	return fmt.Sprintf("#%d [v%d%s; %s; %s] %s", it.ID, it.Version, where, src, it.At.Format("2006-01-02"), strings.TrimSpace(it.Text))
}

const refineSystem = `You improve a ` + brand.Name + ` pipeline: a YAML workflow that runs Claude Code agents, scripts and human check-ins on a piece of work. You are given the pipeline's files and feedback a person left on the work it produced. Propose the smallest set of changes that addresses the feedback well.

Rules:
- Every change must cite the feedback ids it addresses. Don't make changes no feedback asks for.
- Prefer sharpening prompts, skills and rules over restructuring the pipeline. Add, split or reorder steps only when the feedback shows a step is doing too much or something is missing.
- Keep the pipeline valid: the same YAML format, every target a real step, done or stop. Agent steps use prompt: (or agent: /<skill> only for skills shown below, or new skills you add under .claude/skills/<name>/SKILL.md).
- Give the complete new content of each changed or new file.
- Feedback you can't or shouldn't act on (one-off problems, things outside the pipeline's control such as the model, feedback already handled) goes in left_alone with a short reason.
- If nothing is worth changing, return no changes and say why in the summary.
- The summary is one or two sentences for a changelog.`

// Schema is the structured output of a refinement.
var Schema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "changes", "left_alone"],
  "properties": {
    "summary": {"type": "string"},
    "changes": {"type": "array", "items": {
      "type": "object", "additionalProperties": false,
      "required": ["path", "content", "why", "addresses"],
      "properties": {
        "path": {"type": "string"},
        "content": {"type": "string"},
        "why": {"type": "string"},
        "addresses": {"type": "array", "items": {"type": "integer"}}
      }}},
    "left_alone": {"type": "array", "items": {
      "type": "object", "additionalProperties": false,
      "required": ["id", "why"],
      "properties": {"id": {"type": "integer"}, "why": {"type": "string"}}}}
  }
}`)

// ErrNoFeedback is returned when there's no open feedback to act on.
var ErrNoFeedback = errors.New("no open feedback")

// Propose asks Claude for a new version addressing the open feedback.
func Propose(ctx context.Context, c Context) (*Proposal, error) {
	items, err := c.Store.Items()
	if err != nil {
		return nil, err
	}
	var open, resolved []history.Item
	for _, it := range items {
		if it.Status == "open" {
			open = append(open, it)
		} else {
			resolved = append(resolved, it)
		}
	}
	if len(open) == 0 {
		return nil, ErrNoFeedback
	}
	files := c.inputs()
	inputs := map[string]bool{}
	var b strings.Builder
	fmt.Fprintf(&b, "Pipeline %q, currently %s.\n\n## Files\n", c.Pipeline, c.Current.Label())
	for _, f := range files {
		inputs[f.Abs] = true
		fmt.Fprintf(&b, "\n### %s (%s)\n```\n%s\n```\n", f.Path, f.Kind, strings.TrimRight(f.Content, "\n"))
	}
	for _, p := range c.Current.Parts {
		if p.Missing {
			fmt.Fprintf(&b, "\n(%s %q is referenced but couldn't be found, e.g. from a plugin: leave it as is.)\n", p.Kind, p.Name)
		}
	}
	b.WriteString("\n## Open feedback (address these)\n")
	for _, it := range open {
		b.WriteString(feedbackLine(it) + "\n")
	}
	if len(resolved) > 0 {
		b.WriteString("\n## Earlier feedback, already addressed or closed (for context)\n")
		for _, it := range resolved[max(0, len(resolved)-30):] {
			line := feedbackLine(it)
			if it.AddressedIn > 0 {
				line += fmt.Sprintf(" (addressed in v%d)", it.AddressedIn)
			}
			b.WriteString(line + "\n")
		}
	}
	b.WriteString(versionHistory(c))
	b.WriteString("\nPaths in changes: use the paths exactly as shown above for existing files; new files go under " + brand.Dir + "/ or .claude/skills/.\n")

	var out Proposal
	cost, err := c.ask(ctx, "refine", refineSystem, b.String(), Schema, &out)
	if err != nil {
		return nil, err
	}
	out.Pipeline, out.FromVersion, out.FromHash, out.At, out.CostUSD = c.Pipeline, c.Current.Version, c.Current.Hash, time.Now().UTC(), cost
	openIDs := map[int]bool{}
	for _, it := range open {
		openIDs[it.ID] = true
	}
	for i := range out.Changes {
		abs, err := c.resolve(out.Changes[i].Path, inputs)
		if err != nil {
			return nil, fmt.Errorf("the proposal changes a file it shouldn't: %w", err)
		}
		out.Changes[i].Path = abs
		var ids []int
		for _, id := range out.Changes[i].Addresses {
			if openIDs[id] {
				ids = append(ids, id)
			}
		}
		out.Changes[i].Addresses = ids
	}
	out.Findings = c.validate(&out)
	return &out, nil
}

func versionHistory(c Context) string {
	vs, _ := c.Store.Versions()
	if len(vs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Version history\n")
	for _, v := range vs {
		line := fmt.Sprintf("v%d %s (%s)", v.Version, v.At.Format("2006-01-02"), v.Source)
		if v.Summary != "" {
			line += ": " + v.Summary
		}
		if len(v.Changed) > 0 {
			line += " [" + strings.Join(v.Changed, "; ") + "]"
		}
		if len(v.Addresses) > 0 {
			line += fmt.Sprintf(" addressing %v", v.Addresses)
		}
		if c.RunsPerVersion != nil {
			line += fmt.Sprintf(" (%d runs)", c.RunsPerVersion[v.Version])
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// validate checks the proposed pipeline files by validating them in a
// scratch copy of the pipeline dirs.
func (c Context) validate(p *Proposal) []pipeline.Finding {
	tmp, err := os.MkdirTemp("", brand.Name+"-refine")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(tmp)
	var dirs []string
	if c.Repo != "" {
		dirs = append(dirs, filepath.Join(c.Repo, brand.Dir, "pipelines"))
	}
	if c.Home != "" {
		dirs = append(dirs, filepath.Join(c.Home, "pipelines"))
	}
	// Copy the search path, later dirs first so earlier ones shadow them.
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, _ := os.ReadDir(dirs[i])
		for _, e := range entries {
			if b, err := os.ReadFile(filepath.Join(dirs[i], e.Name())); err == nil && !e.IsDir() {
				os.WriteFile(filepath.Join(tmp, e.Name()), b, 0o644)
			}
		}
	}
	for _, ch := range p.Changes {
		for _, d := range dirs {
			if filepath.Dir(ch.Path) == d && (strings.HasSuffix(ch.Path, ".yml") || strings.HasSuffix(ch.Path, ".yaml")) {
				os.WriteFile(filepath.Join(tmp, filepath.Base(ch.Path)), []byte(ch.Content), 0o644)
			}
		}
	}
	_, findings := pipeline.NewLoader(tmp).Validate(c.Pipeline, pipeline.Options{AgentCheck: c.Agents.Check})
	return findings
}

// Apply writes the proposal's files.
func Apply(p *Proposal) error {
	for _, ch := range p.Changes {
		if err := os.MkdirAll(filepath.Dir(ch.Path), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(ch.Path); err == nil {
			mode = st.Mode().Perm()
		} else if strings.Contains(ch.Path, string(filepath.Separator)+"bin"+string(filepath.Separator)) {
			mode = 0o755
		}
		if err := os.WriteFile(ch.Path, []byte(ch.Content), mode); err != nil {
			return err
		}
	}
	return nil
}

// Save writes the proposal (JSON, plus a readable markdown summary) to the
// history's proposals dir and returns the markdown path.
func Save(p *Proposal, store *history.Store, display func(string) string) (string, error) {
	dir := filepath.Join(store.Dir, history.ProposalsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stem := filepath.Join(dir, p.At.Format("20060102-150405"))
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(stem+".json", append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Proposal for %s (from v%d)\n\n%s\n\n", p.Pipeline, p.FromVersion, p.Summary)
	for _, ch := range p.Changes {
		fmt.Fprintf(&md, "## %s\n\n%s\n\nAddresses: %v\n\n", display(ch.Path), ch.Why, ch.Addresses)
	}
	if len(p.LeftAlone) > 0 {
		md.WriteString("## Left alone\n\n")
		for _, s := range p.LeftAlone {
			fmt.Fprintf(&md, "- #%d: %s\n", s.ID, s.Why)
		}
	}
	return stem + ".md", os.WriteFile(stem+".md", []byte(md.String()), 0o644)
}

// --- report ------------------------------------------------------------------

const reportSystem = `You analyse feedback a person left on the work a ` + brand.Name + ` pipeline produced, across the pipeline's versions. You don't grade the work yourself: you read their judgements and look for trends.

Write a short markdown report:
- Recurring themes, with the feedback ids behind each.
- Per version change, what happened after it: which complaints stopped, which continued, and anything new (possible regressions). Read counts relative to how many runs each version had.
- Overall: improving, flat or regressing, and how sure you are given how much feedback there is.
- The one or two things most worth addressing next.
Be concrete and brief. Don't invent feedback.`

// ReportSchema is the structured output of a report.
var ReportSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["report"],"properties":{"report":{"type":"string"}}}`)

// MinFeedback is how much feedback a report needs to be worth writing.
const MinFeedback = 3

// ErrNotEnough is returned when there's too little feedback for a report.
var ErrNotEnough = errors.New("not enough feedback yet")

// Report asks Claude for a trend analysis.
func Report(ctx context.Context, c Context) (string, float64, error) {
	items, err := c.Store.Items()
	if err != nil {
		return "", 0, err
	}
	if len(items) < MinFeedback {
		return "", 0, fmt.Errorf("%w (%d of %d pieces)", ErrNotEnough, len(items), MinFeedback)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Pipeline %q, currently %s.\n", c.Pipeline, c.Current.Label())
	b.WriteString(versionHistory(c))
	b.WriteString("\n## All feedback\n")
	for _, it := range items {
		line := feedbackLine(it) + " (" + it.Status
		if it.AddressedIn > 0 {
			line += fmt.Sprintf(" in v%d", it.AddressedIn)
		}
		b.WriteString(line + ")\n")
	}
	var out struct {
		Report string `json:"report"`
	}
	cost, err := c.ask(ctx, "report", reportSystem, b.String(), ReportSchema, &out)
	if err != nil {
		return "", cost, err
	}
	return strings.TrimSpace(out.Report), cost, nil
}

// SaveReport writes a report to the history's reports dir.
func SaveReport(store *history.Store, md string) (string, error) {
	dir := filepath.Join(store.Dir, history.ReportsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, time.Now().Format("2006-01-02-150405")+".md")
	return path, os.WriteFile(path, []byte(md+"\n"), 0o644)
}

// ask runs one non-interactive Claude call with a structured result.
func (c Context) ask(ctx context.Context, step, system, prompt string, schema json.RawMessage, out any) (float64, error) {
	cli := c.CLI
	if cli == "" {
		cli = "claude"
	}
	ad, err := c.Agents.Get(cli)
	if err != nil {
		return 0, err
	}
	workdir := c.Repo
	if workdir == "" {
		workdir = c.Home
	}
	req := agent.Request{
		Workdir: workdir, Prompt: prompt, SystemAppend: system, Model: c.Model,
		Permission: agent.Permission{Mode: "plan"}, OutputSchema: schema,
		Timeout: 15 * time.Minute, Env: c.Env, RunID: c.Pipeline, Step: step, VisitNumber: 1,
	}
	var cost float64
	for attempt := 0; attempt < 2; attempt++ {
		req.Attempt = attempt
		resp, err := ad.Run(ctx, req, nil)
		cost += resp.CostUSD
		if err != nil {
			return cost, err
		}
		if resp.IsError {
			return cost, fmt.Errorf("claude: %s", resp.ErrorText)
		}
		verr := agent.ValidateOutput(schema, resp.Structured)
		if verr == nil {
			return cost, json.Unmarshal(resp.Structured, out)
		}
		if attempt == 0 && resp.SessionID != "" {
			req.ResumeID, req.Prompt = resp.SessionID, agent.CorrectionPrompt(verr.Error())
			continue
		}
		return cost, fmt.Errorf("claude's answer wasn't in the expected shape: %v", verr)
	}
	return cost, errors.New("unreachable")
}
