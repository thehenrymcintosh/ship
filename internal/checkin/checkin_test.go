package checkin

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
	"github.com/thehenrymcintosh/ship/internal/workspace"
)

func finished(seq int, step, typ, outcome string) store.VisitSummary {
	t := time.Now()
	return store.VisitSummary{Seq: seq, Step: step, Type: typ, Outcome: outcome, Finished: &t}
}

func TestClassify(t *testing.T) {
	review := finished(1, "review", "agent", "changes")
	checks := finished(1, "checks", "run", "fail")
	denied := finished(1, "impl", "agent", "done")
	denied.PermissionDenials = 2
	ask := &store.Ask{Seq: 2, Kind: store.AskKindAsk}
	for name, tc := range map[string]struct {
		s    store.RunSnapshot
		want Kind
	}{
		"running":            {store.RunSnapshot{Status: store.StatusRunning}, ""},
		"needs attention":    {store.RunSnapshot{Status: store.StatusNeedsAttention}, Problem},
		"review findings":    {store.RunSnapshot{Status: store.StatusAsking, PendingAsk: ask, Visits: []store.VisitSummary{review}}, Decision},
		"after failing step": {store.RunSnapshot{Status: store.StatusAsking, PendingAsk: ask, Visits: []store.VisitSummary{checks}}, Problem},
		"after denials":      {store.RunSnapshot{Status: store.StatusAsking, PendingAsk: ask, Visits: []store.VisitSummary{denied}}, Problem},
		"split review":       {store.RunSnapshot{Status: store.StatusAsking, PendingAsk: &store.Ask{Seq: 2, Kind: store.AskKindSplitReview}, Visits: []store.VisitSummary{checks}}, Decision},
	} {
		if got := Classify(&tc.s); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	if Problem.Label() != "Something broke" || Decision.Label() != "Decision" {
		t.Fatal("labels")
	}
}

func raw(s ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, x := range s {
		out = append(out, json.RawMessage(x))
	}
	return out
}

func TestToolPatterns(t *testing.T) {
	got := ToolPatterns(raw(
		`{"tool_name":"Bash","tool_input":{"command":"go test ./... -run X"}}`,
		`{"tool_name":"Bash","tool_input":{"command":"CGO_ENABLED=0 go test ./internal/..."}}`,
		`{"tool_name":"Bash","tool_input":{"command":"psql -c 'select 1' && echo ok"}}`,
		`{"tool_name":"Bash","tool_input":{"command":"git -C x status"}}`,
		`{"tool_name":"WebFetch","tool_input":{"url":"https://pkg.go.dev/net/http"}}`,
		`{"tool_name":"Edit","tool_input":{"file_path":"/x"}}`,
		`{"tool_name":"mcp__db__query"}`,
		`not json`,
	))
	want := []string{"Bash(go test *)", "Bash(psql *)", "Bash(git *)", "WebFetch(domain:pkg.go.dev)", "Edit", "mcp__db__query"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestFailingTests(t *testing.T) {
	out := strings.Join([]string{
		"=== RUN   TestLimiter",
		"--- FAIL: TestLimiter (0.01s)",
		"    --- FAIL: TestLimiter/burst (0.00s)",
		"--- FAIL: TestLimiter (0.01s)",
		"FAIL\tgithub.com/x/y\t0.3s",
		"FAILED tests/test_api.py::test_create - AssertionError: 1 != 2",
		"  ✕ renders the header (12 ms)",
		"test parser::tests::empty ... FAILED",
		"rspec ./spec/models/user_spec.rb:12 # User is valid",
		"ok  \tgithub.com/x/z\t0.1s",
	}, "\n")
	want := []string{"TestLimiter", "TestLimiter/burst", "tests/test_api.py::test_create", "renders the header", "parser::tests::empty", "./spec/models/user_spec.rb:12"}
	if got := FailingTests(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
	if FailingTests("all good\nok\n") != nil {
		t.Fatal("found tests in passing output")
	}
}

func TestDiagnose(t *testing.T) {
	attention := func(reason string, v *store.VisitSummary) Input {
		s := &store.RunSnapshot{Status: store.StatusNeedsAttention, StatusReason: reason, Branch: "b"}
		if v != nil {
			s.Visits = []store.VisitSummary{*v}
		}
		return Input{Snap: s, Visit: v}
	}
	errVisit := func(step, typ, reason string) *store.VisitSummary {
		v := finished(3, step, typ, "error")
		v.Error = &store.StepError{Reason: reason, Message: "it went wrong"}
		return &v
	}

	// A missing worktree comes first: nothing else can run without it.
	in := attention("worktree_missing: /w is gone", nil)
	in.Snap.WorkspaceMissing, in.Snap.Workspace = true, &workspace.Lease{Path: "/w"}
	if d := Diagnose(in); d == nil || d.Fix.Kind != FixReacquire || !strings.Contains(d.Explain, "/w") {
		t.Fatalf("worktree: %+v", d)
	}

	// Budget: raise by half (at least $1) and retry.
	in = attention("budget reached at impl (the run has spent $4.10 of its $4.00 budget)", errVisit("impl", "agent", "run_budget"))
	in.Snap.CostUSD, in.BudgetUSD = 4.1, 4
	if d := Diagnose(in); d == nil || d.Fix.Kind != FixBudget || d.Fix.Value != "2" || d.Fix.Label != "Raise the budget by $2 and retry" {
		t.Fatalf("budget: %+v %+v", d, d.Fix)
	}
	in.BudgetUSD, in.BudgetTokens, in.Snap.Tokens = 0, 1_000_000, 1_000_050
	if d := Diagnose(in); d == nil || d.Fix.Tokens != 500_000 || !strings.Contains(d.Fix.Label, "500k tokens") {
		t.Fatalf("token budget: %+v", d.Fix)
	}

	// Usage limit: says when it resets.
	in = attention("error at impl: rate_limited: …", errVisit("impl", "agent", "rate_limited"))
	in.LimitResets = time.Now().Add(90 * time.Minute)
	if d := Diagnose(in); d == nil || d.Headline != "Claude's usage limit was reached" || !strings.Contains(d.Explain, "resets at "+in.LimitResets.Local().Format("15:04")) || d.Fix.Kind != FixRetry {
		t.Fatalf("limit: %+v", d)
	}
	in = attention(LimitInterrupted+"; carries on when ship restarts", nil)
	if d := Diagnose(in); d == nil || !strings.Contains(d.Explain, "reset time shows here") {
		t.Fatalf("limit without a reset time: %+v", d)
	}

	// Permission denials, at a check-in after the agent.
	v := finished(1, "impl", "agent", "stuck")
	v.PermissionDenials = 2
	in = Input{Snap: &store.RunSnapshot{Status: store.StatusAsking}, Visit: &v,
		Denials: raw(`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`, `{"tool_name":"Bash","tool_input":{"command":"go test -run X ./..."}}`)}
	if d := Diagnose(in); d == nil || d.Fix.Kind != FixAllowTool || d.Fix.Value != "Bash(go test *)" || d.Fix.Label != "Allow Bash(go test *) for this pipeline" ||
		d.Headline != "impl wasn't allowed to run 2 tool calls" {
		t.Fatalf("denials: %+v", d)
	}

	// Timeout: double it.
	in = attention("error at impl: timeout: timed out after 20m", errVisit("impl", "agent", "timeout"))
	in.Timeout = 20 * time.Minute
	if d := Diagnose(in); d == nil || d.Fix.Kind != FixTimeout || d.Fix.Value != "40m" || !strings.Contains(d.Explain, "its 20m timeout") {
		t.Fatalf("timeout: %+v", d)
	}
	in.Timeout = 45 * time.Minute
	if d := Diagnose(in); d.Fix.Value != "1h30m" {
		t.Fatalf("timeout: %+v", d.Fix)
	}

	// A failing script lists its failing tests, at a check-in too.
	checks := finished(1, "checks", "run", "fail")
	in = Input{Snap: &store.RunSnapshot{Status: store.StatusAsking}, Visit: &checks, Output: "--- FAIL: TestA (0s)\n--- FAIL: TestB (0s)\n"}
	if d := Diagnose(in); d == nil || !reflect.DeepEqual(d.Tests, []string{"TestA", "TestB"}) || d.Headline != "checks failed: 2 tests" {
		t.Fatalf("script: %+v", d)
	}

	// Anything else that needs attention can be retried.
	in = attention("error at impl: agent_error: boom", errVisit("impl", "agent", "agent_error"))
	if d := Diagnose(in); d == nil || d.Fix.Kind != FixRetry || d.Headline != "impl stopped with an error (agent_error)" {
		t.Fatalf("other: %+v", d)
	}

	// A check-in after a clean agent step is a decision, not a diagnosis.
	ok := finished(1, "review", "agent", "changes")
	if d := Diagnose(Input{Snap: &store.RunSnapshot{Status: store.StatusAsking}, Visit: &ok}); d != nil {
		t.Fatalf("diagnosed a decision: %+v", d)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{90 * time.Second: "90s", 20 * time.Minute: "20m", 2 * time.Hour: "2h", 90 * time.Minute: "1h30m"} {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: %s", d, got)
		}
	}
}
