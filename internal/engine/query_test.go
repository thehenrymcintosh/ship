package engine

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// Of several edges between two steps, only the outcome taken lights up.
func TestMarkTakenMatchesOutcome(t *testing.T) {
	g := pipeline.Graph{Edges: []pipeline.GraphEdge{
		{From: "review", To: "checks", Label: "major", Kind: "outcome"},
		{From: "review", To: "checks", Label: "patch", Kind: "outcome"},
		{From: "review", To: "check-in", Label: "error", Kind: "error"},
		{From: "review", To: "check-in", Label: "stuck", Kind: "outcome"},
		{From: "checks", To: "implement", Label: "fail", Kind: "outcome"},
	}}
	ev := func(tr store.Transition) store.Event {
		b, _ := json.Marshal(tr)
		return store.Event{Type: store.EvTransition, Data: b}
	}
	markTaken(&g, []store.Event{
		ev(store.Transition{From: "review", To: "checks", Outcome: "patch", Reason: store.ReasonNormal}),
		ev(store.Transition{From: "review", To: "check-in", Outcome: "error", Reason: store.ReasonError}),
		ev(store.Transition{From: "checks", To: "implement", Reason: store.ReasonManual}), // a goto: no matching label
	})
	want := []bool{false, true, true, false, true}
	for i, e := range g.Edges {
		if e.Taken != want[i] {
			t.Errorf("%s -%s-> %s: taken %v, want %v", e.From, e.Label, e.To, e.Taken, want[i])
		}
	}
}

// A conversation idle past the cache's lifetime, or grown large, isn't
// resumed: re-sending all of it costs more than starting from the handover.
func TestStaleSession(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	s := &store.RunSnapshot{Visits: []store.VisitSummary{
		{Seq: 1, SessionID: "a", Finished: ago(2 * time.Hour), Usage: &store.TokenUsage{CacheWrite: 10_000}},
		{Seq: 2, SessionID: "b", Finished: ago(time.Minute), Usage: &store.TokenUsage{CacheWrite: 100_000, Output: 10_000}},
		{Seq: 3, SessionID: "b", Finished: ago(time.Minute), Usage: &store.TokenUsage{CacheWrite: 60_000, CacheRead: 5_000_000}},
		{Seq: 4, SessionID: "c", Finished: ago(time.Minute), Usage: &store.TokenUsage{CacheWrite: 20_000, CacheRead: 5_000_000}},
		{Seq: 5, Step: "triage", SessionID: "d", Finished: ago(9 * time.Second), Usage: &store.TokenUsage{CacheWrite: 5_000}},
	}}
	f := freshRules{idle: defaultFreshAfterIdle, tokens: defaultFreshAfterTokens, skill: func(step string) bool { return step == "triage" }}
	if why := staleSession(s, &s.Visits[0], now, f); !strings.Contains(why, "idle") {
		t.Errorf("idle 2h: %q", why)
	}
	if why := staleSession(s, &s.Visits[2], now, f); !strings.Contains(why, "170k") {
		t.Errorf("170k across the session's visits: %q", why)
	}
	// Cache reads don't grow the conversation.
	if why := staleSession(s, &s.Visits[3], now, f); why != "" {
		t.Errorf("small, recent session should resume: %q", why)
	}
	// A conversation resumed after a skill step misses the cache (#3).
	if why := staleSession(s, &s.Visits[4], now, f); !strings.Contains(why, "triage step's skill") {
		t.Errorf("after a skill step: %q", why)
	}
	// 0 disables a threshold.
	if why := staleSession(s, &s.Visits[2], now, freshRules{}); why != "" {
		t.Errorf("thresholds disabled: %q", why)
	}
	if why := staleSession(s, &s.Visits[0], now, freshRules{idle: 3 * time.Hour}); why != "" {
		t.Errorf("idle 2h under a 3h threshold: %q", why)
	}
	if h := freshHandover("/run", sessionPlan{kind: "fresh", last: &store.VisitSummary{Dir: "0003-implement"}}); h != filepath.Join("/run", store.VisitsDir, "0003-implement", "handover.md") {
		t.Errorf("handover %q", h)
	}
}

// The thresholds come from config's agent settings (0 disables), and a
// step with `agent:` runs a skill.
func TestFreshRulesFor(t *testing.T) {
	skill, idle, zero := "/review", pipeline.Duration(2*time.Hour), pipeline.Tokens(0)
	st := &pipeline.Step{}
	r := &runner{pipe: &pipeline.Pipeline{Steps: map[string]*pipeline.Step{"review": {Agent: &skill}, "fix": st}}}
	if f := r.freshRulesFor(st); f.idle != time.Hour || f.tokens != 150_000 || !f.skill("review") || f.skill("fix") || f.skill("gone") {
		t.Errorf("defaults: %+v", f)
	}
	r.cfg.Agent.FreshAfterIdle, r.cfg.Agent.FreshAfterTokens = &idle, &zero
	if f := r.freshRulesFor(st); f.idle != 2*time.Hour || f.tokens != 0 {
		t.Errorf("configured: %+v", f)
	}
}
