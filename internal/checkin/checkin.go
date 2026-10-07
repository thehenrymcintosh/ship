// Package checkin works out what a run waiting for a person needs: whether
// something broke (a problem) or a choice is to be made (a decision), and,
// for known failures, what went wrong and the one-click fix. Its
// diagnosers are small deterministic functions over the run's snapshot, the
// visit that went wrong and what it left behind; no agent is involved.
package checkin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
)

// Kind is what a card asks of the person.
type Kind string

// Kinds.
const (
	Problem  Kind = "problem"  // something broke
	Decision Kind = "decision" // a choice is to be made
)

// Label is the kind's wording on cards, rows and the CLI.
func (k Kind) Label() string {
	if k == Problem {
		return "Something broke"
	}
	return "Decision"
}

// Classify says whether a run in the inbox waits on a problem or a
// decision ("" when it isn't waiting for anyone). It needs only the
// snapshot, so lists can show it cheaply.
func Classify(s *store.RunSnapshot) Kind {
	switch s.Status {
	case store.StatusNeedsAttention:
		return Problem
	case store.StatusAsking:
		if pa := s.PendingAsk; pa != nil && pa.Kind == store.AskKindAsk {
			if pv := Before(s, pa.Seq); pv != nil && Failed(pv) {
				return Problem
			}
		}
		return Decision
	}
	return ""
}

// Failed reports whether a finished visit went wrong: an error, a failing
// script, or tool calls the agent wasn't allowed to make.
func Failed(v *store.VisitSummary) bool {
	return v.Error != nil || v.Outcome == "error" || v.Outcome == "fail" || v.PermissionDenials > 0
}

// Before returns the last finished visit before seq, or nil.
func Before(s *store.RunSnapshot, seq int) *store.VisitSummary {
	for i := len(s.Visits) - 1; i >= 0; i-- {
		if v := &s.Visits[i]; v.Seq < seq && v.Finished != nil {
			return v
		}
	}
	return nil
}

// Fix kinds: each is one endpoint the card's button posts to.
const (
	FixAllowTool = "allow_tool" // add allowed_tools to the pipeline, then retry
	FixTimeout   = "timeout"    // raise the step's timeout, then retry
	FixBudget    = "budget"     // raise the run's budget, then retry
	FixReacquire = "reacquire"  // get a fresh worktree
	FixRetry     = "retry"      // run the step again
)

// Fix is a diagnosis's one-click remedy.
type Fix struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Value is what the fix applies: tool patterns (one per line), a
	// duration, or dollars to add.
	Value string `json:"value,omitempty"`
	// Tokens to add, for a budget held by its token limit.
	Tokens int64 `json:"tokens,omitempty"`
}

// Diagnosis explains a known failure.
type Diagnosis struct {
	Headline string `json:"headline"`
	Explain  string `json:"explain"`
	Fix      *Fix   `json:"fix,omitempty"`
	// Tests are the failing tests found in a script's output.
	Tests []string `json:"tests,omitempty"`
	// ResetsAt is when a usage limit resets, when known.
	ResetsAt time.Time `json:"resets_at,omitempty"`
}

// Input is what the diagnosers look at.
type Input struct {
	Snap *store.RunSnapshot
	// Visit is the visit that went wrong: the last one when the run needs
	// attention, the one before the check-in otherwise.
	Visit *store.VisitSummary
	// Denials are the visit's raw permission denials (result.json).
	Denials []json.RawMessage
	// Output is the end of a script's output.
	Output string
	// Timeout is the step's timeout now.
	Timeout time.Duration
	// Budget is the run's budget, raises included (0 = none).
	BudgetUSD    float64
	BudgetTokens int64
	// LimitResets is when the account's usage limit resets, if known.
	LimitResets time.Time
}

// LimitInterrupted starts the status reason of a run that was stopped while
// it waited out a usage limit (see the engine).
const LimitInterrupted = "interrupted while waiting out a usage limit"

// Diagnose runs the diagnosers in order and returns the first that
// recognises the failure, or nil.
func Diagnose(in Input) *Diagnosis {
	for _, d := range []func(Input) *Diagnosis{worktreeMissing, budgetReached, usageLimit, permissionDenied, timedOut, scriptFailed, otherError} {
		if out := d(in); out != nil {
			return out
		}
	}
	return nil
}

func attention(in Input) bool { return in.Snap.Status == store.StatusNeedsAttention }

func errReason(v *store.VisitSummary) string {
	if v == nil || v.Error == nil {
		return ""
	}
	return v.Error.Reason
}

func worktreeMissing(in Input) *Diagnosis {
	if !in.Snap.WorkspaceMissing {
		return nil
	}
	path := "The worktree"
	if in.Snap.Workspace != nil {
		path = in.Snap.Workspace.Path
	}
	return &Diagnosis{
		Headline: "The run's worktree is gone",
		Explain:  fmt.Sprintf("%s was removed while the run was using it. Re-acquiring checks out the run's branch (%s) again in a fresh worktree; anything not committed there is lost.", path, in.Snap.Branch),
		Fix:      &Fix{Kind: FixReacquire, Label: "Re-acquire the worktree"},
	}
}

func budgetReached(in Input) *Diagnosis {
	if !attention(in) || (!strings.HasPrefix(in.Snap.StatusReason, "budget reached") && errReason(in.Visit) != "run_budget") {
		return nil
	}
	d := &Diagnosis{Headline: "The run reached its budget"}
	f := &Fix{Kind: FixBudget}
	switch {
	case in.BudgetUSD > 0 && in.Snap.CostUSD >= in.BudgetUSD:
		add := math.Max(1, math.Ceil(in.BudgetUSD/2))
		f.Value = fmt.Sprintf("%g", add)
		f.Label = fmt.Sprintf("Raise the budget by $%g and retry", add)
		d.Explain = fmt.Sprintf("It has spent $%.2f of its $%.2f. Raising the budget lets the step run again; nothing else changes.", in.Snap.CostUSD, in.BudgetUSD)
	case in.BudgetTokens > 0:
		add := max(in.BudgetTokens/2, 100_000)
		f.Tokens = add
		f.Label = fmt.Sprintf("Raise the budget by %s tokens and retry", shortTokens(add))
		d.Explain = fmt.Sprintf("It has used %s of its %s tokens. Raising the budget lets the step run again; nothing else changes.", shortTokens(in.Snap.Tokens), shortTokens(in.BudgetTokens))
	default:
		f = nil
		d.Explain = in.Snap.StatusReason
	}
	d.Fix = f
	return d
}

func shortTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + "m"
	case n >= 1000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e3), ".0") + "k"
	}
	return fmt.Sprint(n)
}

func usageLimit(in Input) *Diagnosis {
	if !attention(in) || (!strings.HasPrefix(in.Snap.StatusReason, LimitInterrupted) && errReason(in.Visit) != "rate_limited") {
		return nil
	}
	d := &Diagnosis{Headline: "Claude's usage limit was reached", ResetsAt: in.LimitResets,
		Fix: &Fix{Kind: FixRetry, Label: "Retry the step"}}
	switch {
	case in.LimitResets.IsZero():
		d.Explain = "Claude kept refusing for a usage or rate limit. Retry once it has reset; the reset time shows here after the next agent step reports it."
	case time.Until(in.LimitResets) <= 0:
		d.Explain = "The limit has reset, so the step can run again."
	default:
		d.Explain = fmt.Sprintf("It resets at %s (in %s). Retrying before then waits for it.", in.LimitResets.Local().Format("15:04 Mon"), time.Until(in.LimitResets).Round(time.Minute))
	}
	return d
}

func permissionDenied(in Input) *Diagnosis {
	v := in.Visit
	if v == nil || v.PermissionDenials == 0 {
		return nil
	}
	pats := ToolPatterns(in.Denials)
	d := &Diagnosis{
		Headline: fmt.Sprintf("%s wasn't allowed to run %s", v.Step, plural(v.PermissionDenials, "tool call")),
		Explain:  "The agent asked to use tools its permissions don't allow, so it may have stopped short.",
	}
	if len(pats) > 0 {
		d.Explain = fmt.Sprintf("The agent asked to use %s, which its permissions don't allow, so it may have stopped short. Allowing it adds it to the pipeline's allowed_tools; then the step runs again.", strings.Join(pats, ", "))
		d.Fix = &Fix{Kind: FixAllowTool, Value: strings.Join(pats, "\n"), Label: "Allow " + strings.Join(pats, ", ") + " for this pipeline"}
	}
	return d
}

func plural(n int, s string) string {
	if n == 1 {
		return "1 " + s
	}
	return fmt.Sprintf("%d %ss", n, s)
}

func timedOut(in Input) *Diagnosis {
	if errReason(in.Visit) != "timeout" {
		return nil
	}
	cur := in.Timeout
	next := 2 * cur
	if cur <= 0 {
		next = time.Hour
	}
	next = next.Round(time.Minute)
	d := &Diagnosis{
		Headline: fmt.Sprintf("%s ran out of time", in.Visit.Step),
		Explain:  "It was stopped at its timeout before it finished.",
		Fix:      &Fix{Kind: FixTimeout, Value: FormatDuration(next), Label: fmt.Sprintf("Raise the timeout to %s and retry", FormatDuration(next))},
	}
	if cur > 0 {
		d.Explain = fmt.Sprintf("It was stopped at its %s timeout before it finished. Raising it writes timeout: %s to the step in the pipeline; then the step runs again.", FormatDuration(cur), FormatDuration(next))
	}
	return d
}

// FormatDuration writes a duration the way pipelines do: 90s, 20m, 1h30m.
func FormatDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0 && d >= time.Hour:
		return fmt.Sprintf("%dh%dm", d/time.Hour, (d%time.Hour)/time.Minute)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func scriptFailed(in Input) *Diagnosis {
	v := in.Visit
	if v == nil || v.Type != "run" || (v.Outcome != "fail" && v.Error == nil) {
		return nil
	}
	tests := FailingTests(in.Output)
	d := &Diagnosis{Tests: tests, Fix: &Fix{Kind: FixRetry, Label: "Run " + v.Step + " again"}}
	switch len(tests) {
	case 0:
		d.Headline = v.Step + " failed"
		d.Explain = "Its script exited with an error; the end of its output is below."
	case 1:
		d.Headline = fmt.Sprintf("%s failed: %s", v.Step, tests[0])
		d.Explain = "One test fails."
	default:
		d.Headline = fmt.Sprintf("%s failed: %d tests", v.Step, len(tests))
		d.Explain = fmt.Sprintf("%d tests fail: %s.", len(tests), strings.Join(tests, ", "))
	}
	if v.Error != nil && v.Error.Message != "" {
		d.Explain += " " + firstLine(v.Error.Message)
	}
	return d
}

func otherError(in Input) *Diagnosis {
	if !attention(in) {
		return nil
	}
	d := &Diagnosis{Headline: in.Snap.StatusReason, Fix: &Fix{Kind: FixRetry, Label: "Retry the step"}}
	if v := in.Visit; v != nil && v.Error != nil {
		d.Headline = fmt.Sprintf("%s stopped with an error (%s)", v.Step, v.Error.Reason)
		d.Explain = firstLine(v.Error.Message)
	}
	if strings.HasPrefix(in.Snap.StatusReason, "interrupted") {
		d.Headline = "The run was interrupted"
		d.Explain = in.Snap.StatusReason
	}
	return d
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// --- permission denials ------------------------------------------------------

// subcommands are CLIs whose first argument names what they do, so a denial
// of "go test ./..." is allowed as Bash(go test *) rather than all of go.
var subcommands = map[string]bool{
	"go": true, "npm": true, "pnpm": true, "yarn": true, "npx": true, "bun": true, "cargo": true, "git": true,
	"gh": true, "make": true, "docker": true, "kubectl": true, "pip": true, "uv": true, "poetry": true,
	"bundle": true, "rails": true, "mix": true, "dotnet": true, "gradle": true, "mvn": true, "swift": true, "deno": true,
}

// ToolPatterns turns Claude's permission denials into allowed_tools
// patterns, deduplicated, in the order they were denied.
func ToolPatterns(denials []json.RawMessage) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range denials {
		var d struct {
			Tool  string         `json:"tool_name"`
			Input map[string]any `json:"tool_input"`
		}
		if json.Unmarshal(raw, &d) != nil || d.Tool == "" {
			continue
		}
		p := toolPattern(d.Tool, d.Input)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func toolPattern(tool string, in map[string]any) string {
	switch tool {
	case "Bash":
		cmd, _ := in["command"].(string)
		if prefix := commandPrefix(cmd); prefix != "" {
			return "Bash(" + prefix + " *)"
		}
	case "WebFetch":
		if s, _ := in["url"].(string); s != "" {
			if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
				return "WebFetch(domain:" + u.Hostname() + ")"
			}
		}
	}
	return tool
}

// commandPrefix is the command a shell line runs (its first, before any
// &&, | or ;), with its subcommand for CLIs that have them; env
// assignments are skipped.
func commandPrefix(cmd string) string {
	if i := strings.IndexAny(cmd, "&|;\n"); i >= 0 {
		cmd = cmd[:i]
	}
	var words []string
	for _, w := range strings.Fields(cmd) {
		if len(words) == 0 && strings.Contains(w, "=") && !strings.HasPrefix(w, "=") {
			continue
		}
		words = append(words, w)
	}
	if len(words) == 0 {
		return ""
	}
	if subcommands[words[0]] && len(words) > 1 && !strings.HasPrefix(words[1], "-") {
		return words[0] + " " + words[1]
	}
	return words[0]
}

// --- failing tests -----------------------------------------------------------

var testPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\s*--- FAIL: (\S+)`),                         // go test
	regexp.MustCompile(`^FAILED (\S+?)(?: - .*)?$`),                   // pytest
	regexp.MustCompile(`^\s*(?:✕|×|✗)\s+(.+?)(?:\s+\(\d+\s*m?s\))?$`), // jest, vitest, mocha
	regexp.MustCompile(`^test (\S+) \.\.\. FAILED`),                   // cargo test
	regexp.MustCompile(`^rspec (\./\S+)`),                             // rspec
}

// FailingTests pulls the names of failing tests out of a script's output,
// for the common test runners, in order and without repeats (20 at most).
func FailingTests(output string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, re := range testPatterns {
			m := re.FindStringSubmatch(line)
			if m == nil || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			out = append(out, m[1])
			if len(out) == 20 {
				return out
			}
			break
		}
	}
	return out
}
