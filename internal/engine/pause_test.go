package engine

import (
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

func TestPauseAtStepBoundary(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: a
workspace: {provider: none}
steps:
  a: {run: "sleep 1", next: b}
  b: {run: "true", next: done}
`})
	s := en.start("p", "", nil, "")
	en.waitFor(s.ID, "a running", func(s *store.RunSnapshot) bool { return len(s.Visits) == 1 })
	if err := en.e.Do(s.ID, Command{Name: CmdPause}); err != nil {
		t.Fatal(err)
	}
	if s2, _ := en.e.Snapshot(s.ID); !s2.PauseRequested {
		t.Fatal("pause should be recorded straight away")
	}
	// a finishes; b doesn't start.
	s = en.waitStatus(s.ID, store.StatusPaused)
	if got := visitTrail(s); got != "a:pass" || s.CurrentStep != "b" {
		t.Fatalf("trail %q step %s", got, s.CurrentStep)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "x"}); KindOf(err) != KindConflict {
		t.Fatalf("answer while paused: %v", err)
	}
	// Survives a restart.
	en.restart()
	s = en.waitStatus(s.ID, store.StatusPaused)
	if err := en.e.Do(s.ID, Command{Name: CmdResume}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "a:pass b:pass" || s.PauseRequested {
		t.Fatalf("trail %q", got)
	}
}

const pauseParent = `version: 1
start: split
steps:
  split:
    split: ""
    next: {ok: build}
  build:
    fanout: child
    mode: parallel
    max_parallel: 1
    next: {done: done, failed: stop}
`

const pauseChild = `version: 1
start: hold
steps:
  hold:
    ask: "go?"
    choices: {go: work}
  work:
    run: "true"
    next: done
`

const threeSlices = `split:
  - outcome: ok
    summary: three
    slices:
      - {key: a, title: A, brief: a, acceptance: [x]}
      - {key: b, title: B, brief: b, acceptance: [x]}
      - {key: c, title: C, brief: c, acceptance: [x]}
`

func TestPauseFanoutParent(t *testing.T) {
	en := newEnv(t, map[string]string{"parent": pauseParent, "child": pauseChild})
	s := en.start("parent", "", nil, threeSlices)
	s = en.waitFor(s.ID, "first slice", func(s *store.RunSnapshot) bool { return len(s.Children) == 1 })
	c1 := en.waitStatus(s.Children[0].ID, store.StatusAsking)

	// Pausing the parent: no new slices; the running one carries on.
	if err := en.e.Do(s.ID, Command{Name: CmdPause}); err != nil {
		t.Fatal(err)
	}
	en.waitStatus(s.ID, store.StatusPaused)
	en.e.Do(c1.ID, Command{Name: CmdAnswer, Choice: "go"})
	en.waitStatus(c1.ID, store.StatusDone)
	en.restart() // still paused, still supervising, after a restart
	s = en.waitStatus(s.ID, store.StatusPaused)
	if len(s.Children) != 1 {
		t.Fatalf("paused parent started another slice: %+v", s.Children)
	}

	// Resume: the next slice starts.
	if err := en.e.Do(s.ID, Command{Name: CmdResume}); err != nil {
		t.Fatal(err)
	}
	s = en.waitFor(s.ID, "second slice", func(s *store.RunSnapshot) bool { return len(s.Children) == 2 && s.Status == store.StatusFannedOut })

	// Pause --all: the running slice pauses too (at its next boundary).
	c2 := en.waitStatus(s.Children[1].ID, store.StatusAsking)
	if err := en.e.Do(s.ID, Command{Name: CmdPause, All: true}); err != nil {
		t.Fatal(err)
	}
	en.waitFor(c2.ID, "child pause requested", func(c *store.RunSnapshot) bool { return c.PauseRequested })
	en.e.Do(c2.ID, Command{Name: CmdAnswer, Choice: "go"})
	en.waitStatus(c2.ID, store.StatusPaused) // held before "work"

	// Resume --all: the slice and the parent carry on to the end.
	if err := en.e.Do(s.ID, Command{Name: CmdResume, All: true}); err != nil {
		t.Fatal(err)
	}
	en.waitStatus(c2.ID, store.StatusDone)
	s = en.waitFor(s.ID, "third slice", func(s *store.RunSnapshot) bool { return len(s.Children) == 3 })
	c3 := en.waitStatus(s.Children[2].ID, store.StatusAsking)
	en.e.Do(c3.ID, Command{Name: CmdAnswer, Choice: "go"})
	en.waitStatus(s.ID, store.StatusDone)
}
