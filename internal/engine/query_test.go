package engine

import (
	"encoding/json"
	"testing"

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
