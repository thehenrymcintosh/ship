package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

const ticketParent = `version: 1
start: split
variables:
  ticket: { from_brief: true, format: '^[A-Z]+-[0-9]+$' }
steps:
  split:
    split: ""
    next: {ok: build}
  build:
    fanout: child
    mode: series
    next: {done: done, failed: stop}
`

const ticketChild = `version: 1
start: work
variables:
  ticket: { from_parent: ticket, format: '^[A-Z]+-[0-9]+$' }
workspace: { branch: "feat/{{vars.ticket}}" }
steps:
  work: {run: "true", next: done}
`

func TestSlicesCarryTheirOwnTickets(t *testing.T) {
	en := newEnv(t, map[string]string{"parent": ticketParent, "child": ticketChild})
	s := en.start("parent", "", map[string]string{"ticket": "API-1"}, `split:
  - outcome: ok
    summary: two tickets
    slices:
      - {key: a, title: A, brief: a, acceptance: [x], vars: {ticket: API-2}}
      - {key: b, title: B, brief: b, acceptance: [x]}
`)
	s = en.waitStatus(s.ID, store.StatusDone)
	want := map[string]string{"a": "API-2", "b": "API-1"}
	for _, c := range s.Children {
		cs, err := en.e.Snapshot(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := cs.Vars["ticket"]; got != want[c.SliceKey] || cs.Branch != "feat/"+want[c.SliceKey] {
			t.Errorf("slice %s: ticket %q branch %q", c.SliceKey, got, cs.Branch)
		}
	}
}

func TestSliceVarsAreChecked(t *testing.T) {
	en := newEnv(t, map[string]string{"parent": ticketParent, "child": ticketChild})
	// The split schema only offers the child's variables.
	s := en.start("parent", "", map[string]string{"ticket": "API-1"}, `split:
  - outcome: ok
    summary: bad
    slices:
      - {key: a, title: A, brief: a, acceptance: [x], vars: {nope: x}}
`)
	s = en.waitFor(s.ID, "split rejected", func(s *store.RunSnapshot) bool {
		lv := s.LastVisit()
		return lv != nil && lv.Step == "split" && lv.Outcome != ""
	})
	if lv := s.LastVisit(); lv.Outcome != "error" {
		t.Fatalf("split with an unknown var: %s %s", lv.Outcome, lv.Summary)
	}
}

func TestChildPipelineRunsOnItsOwn(t *testing.T) {
	en := newEnv(t, map[string]string{"parent": ticketParent, "child": ticketChild})
	if _, err := en.e.Start(context.Background(), StartRequest{Repo: en.repo, Pipeline: "child", Brief: []byte("---\ntitle: T\n---\nx\n")}); err == nil || !strings.Contains(err.Error(), `missing variable "ticket"`) {
		t.Fatalf("no ticket: %v", err)
	}
	s := en.start("child", "", map[string]string{"ticket": "API-9"}, "")
	s = en.waitStatus(s.ID, store.StatusDone)
	if s.Vars["ticket"] != "API-9" || s.Branch != "feat/API-9" || s.Parent != nil {
		t.Fatalf("%+v", s)
	}
}
