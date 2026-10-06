package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

const afterPipeline = `version: 1
start: check-in
steps:
  check-in:
    ask: go?
    choices: {go: done, abandon: stop}
`

func (en *env) startAfter(after string, stack bool) *store.RunSnapshot {
	en.t.Helper()
	snap, err := en.e.Start(context.Background(), StartRequest{Repo: en.repo, Pipeline: "p", Brief: []byte("---\ntitle: Next\n---\nbody\n"), After: after, Stack: stack})
	if err != nil {
		en.t.Fatalf("start after %s: %v", after, err)
	}
	return snap
}

func checkWaiting(t *testing.T, s *store.RunSnapshot, on string) {
	t.Helper()
	if s.Status != store.StatusWaiting || s.WaitingOn != on || s.StatusReason != store.WaitingOnReason(on) || s.Workspace != nil || len(s.Visits) != 0 {
		t.Fatalf("want waiting on %s with no worktree: %s %q on=%q ws=%v visits=%s", on, s.Status, s.StatusReason, s.WaitingOn, s.Workspace, visitTrail(s))
	}
}

func TestAfterStartsWhenTheRunIsDone(t *testing.T) {
	en := newEnv(t, map[string]string{"p": afterPipeline})
	first := en.start("p", "", nil, "")
	first = en.waitStatus(first.ID, store.StatusAsking)
	next := en.startAfter(first.ID, true)
	checkWaiting(t, next, first.ID)
	if next.After != first.ID || !next.AfterStack {
		t.Fatalf("after %q stack %v", next.After, next.AfterStack)
	}

	// The wait survives a restart.
	en.restart()
	next, _ = en.e.Snapshot(next.ID)
	checkWaiting(t, next, first.ID)
	if !en.e.Active(next.ID) {
		t.Fatal("the waiting run wasn't recovered")
	}

	if err := en.e.Do(first.ID, Command{Name: CmdAnswer, Choice: "go"}); err != nil {
		t.Fatal(err)
	}
	en.waitStatus(first.ID, store.StatusDone)
	next = en.waitStatus(next.ID, store.StatusAsking)
	if next.WaitingOn != "" || next.Workspace == nil || next.PendingAsk == nil || next.PendingAsk.Kind != store.AskKindAsk {
		t.Fatalf("next didn't start: on=%q ws=%v ask=%+v", next.WaitingOn, next.Workspace, next.PendingAsk)
	}
	// Stacked: it branches from the first run's branch.
	if next.Base != first.Branch || next.Workspace.Base != first.Branch {
		t.Fatalf("base %q (lease %q), want %q", next.Base, next.Workspace.Base, first.Branch)
	}
}

func TestAfterAsksWhenTheRunDoesntFinishDone(t *testing.T) {
	en := newEnv(t, map[string]string{"p": afterPipeline})
	first := en.start("p", "", nil, "")
	en.waitStatus(first.ID, store.StatusAsking)
	next := en.startAfter(first.ID, false)
	cancelled := en.startAfter(first.ID, false)
	if err := en.e.Do(first.ID, Command{Name: CmdAnswer, Choice: "abandon"}); err != nil {
		t.Fatal(err)
	}
	en.waitStatus(first.ID, store.StatusStopped)
	for _, id := range []string{next.ID, cancelled.ID} {
		s := en.waitStatus(id, store.StatusAsking)
		if s.PendingAsk == nil || s.PendingAsk.Kind != store.AskKindAfter || !strings.Contains(s.PendingAsk.Question, "stopped") || s.Workspace != nil {
			t.Fatalf("want the start-anyway question: %+v ws=%v", s.PendingAsk, s.Workspace)
		}
	}

	// The question survives a restart, and isn't asked twice.
	en.restart()
	next = en.waitStatus(next.ID, store.StatusAsking)
	if n := countEvents(t, en, next.ID, store.EvAskPending); n != 1 {
		t.Fatalf("asked %d times", n)
	}
	if err := en.e.Do(next.ID, Command{Name: CmdAnswer, Choice: "maybe"}); KindOf(err) != KindInvalid {
		t.Fatalf("an unknown choice should be invalid: %v", err)
	}
	if err := en.e.Do(next.ID, Command{Name: CmdAnswer, Choice: store.AfterStartAnyway}); err != nil {
		t.Fatal(err)
	}
	next = en.waitFor(next.ID, "its own check-in", func(s *store.RunSnapshot) bool {
		return s.Status == store.StatusAsking && s.PendingAsk != nil && s.PendingAsk.Kind == store.AskKindAsk
	})
	if next.Base != "main" || next.Workspace == nil {
		t.Fatalf("base %q ws %v", next.Base, next.Workspace)
	}

	if err := en.e.Do(cancelled.ID, Command{Name: CmdAnswer, Choice: store.AfterCancel}); err != nil {
		t.Fatal(err)
	}
	en.waitStatus(cancelled.ID, store.StatusCancelled)
}

func TestAfterStartNowAndCancel(t *testing.T) {
	en := newEnv(t, map[string]string{"p": afterPipeline})
	first := en.start("p", "", nil, "")
	en.waitStatus(first.ID, store.StatusAsking)
	now := en.startAfter(first.ID, false)
	later := en.startAfter(first.ID, false)
	if err := en.e.Do(now.ID, Command{Name: CmdRetry}); KindOf(err) != KindConflict {
		t.Fatalf("retry while waiting: %v", err)
	}
	if err := en.e.Do(now.ID, Command{Name: CmdStartNow}); err != nil {
		t.Fatal(err)
	}
	s := en.waitStatus(now.ID, store.StatusAsking)
	if s.Workspace == nil || s.PendingAsk == nil || s.PendingAsk.Kind != store.AskKindAsk {
		t.Fatalf("didn't start: %+v", s.PendingAsk)
	}
	if err := en.e.Do(later.ID, Command{Name: CmdCancel}); err != nil {
		t.Fatal(err)
	}
	en.waitStatus(later.ID, store.StatusCancelled)
	if s, _ := en.e.Snapshot(first.ID); s.Status != store.StatusAsking {
		t.Fatalf("the first run was touched: %s", s.Status)
	}
}

func TestAfterValidation(t *testing.T) {
	en := newEnv(t, map[string]string{"p": afterPipeline})
	req := StartRequest{Repo: en.repo, Pipeline: "p", Brief: []byte("---\ntitle: x\n---\n"), After: "nope"}
	if _, err := en.e.Start(context.Background(), req); KindOf(err) != KindInvalid {
		t.Fatalf("unknown run: %v", err)
	}
	req.After, req.Stack = "", true
	if _, err := en.e.Start(context.Background(), req); KindOf(err) != KindInvalid {
		t.Fatalf("stack without after: %v", err)
	}
}

func countEvents(t *testing.T, en *env, id, typ string) int {
	t.Helper()
	evs, _, err := store.ReadEvents(en.st.RunDir(id))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// --- starting a fanout's slices out of turn ---------------------------------

const earlyParent = `version: 1
start: split
steps:
  split:
    split: /split
    review: true
    next: {ok: build}
  build:
    fanout: slice
    mode: series
    stack: %v
    next: {done: done, failed: stop}
`

// Each slice holds until its file exists in the test's dir.
const earlySlice = `version: 1
start: hold
steps:
  hold:
    wait: 'test -f %s/go-{{slice.key}} && echo go || echo hold'
    every: 50ms
    next: {go: done}
`

func newEarly(t *testing.T, stack bool) (*env, string) {
	gate := t.TempDir()
	en := newEnv(t, map[string]string{
		"feature": fmt.Sprintf(earlyParent, stack),
		"slice":   fmt.Sprintf(earlySlice, gate),
	})
	return en, gate
}

func (en *env) approveSlices(n int) *store.RunSnapshot {
	en.t.Helper()
	s := en.start("feature", "", nil, e2eScript(n))
	en.waitStatus(s.ID, store.StatusAsking)
	if err := en.e.Do(s.ID, Command{Name: CmdSplitReview, Action: "approve"}); err != nil {
		en.t.Fatal(err)
	}
	return en.waitFor(s.ID, "slice 1", func(s *store.RunSnapshot) bool { return len(s.Children) == 1 })
}

func sliceStates(en *env, id string) string {
	s, _ := en.e.Snapshot(id)
	var parts []string
	for _, ss := range en.e.Slices(s) {
		st := string(ss.Status)
		if ss.CanStart {
			st += "+"
		}
		parts = append(parts, fmt.Sprintf("%d:%s", ss.Number, st))
	}
	return strings.Join(parts, " ")
}

func TestStartSliceEarlyStacked(t *testing.T) {
	en, gate := newEarly(t, true)
	s := en.approveSlices(3)
	en.waitStatus(s.Children[0].ID, store.StatusWaiting)
	if got := sliceStates(en, s.ID); got != "1:waiting 2:pending+ 3:pending" {
		t.Fatalf("slices %s", got)
	}
	err := en.e.Do(s.ID, Command{Name: CmdStartSlice, Slice: 3})
	if KindOf(err) != KindConflict || !strings.Contains(err.Error(), "stacks on slice 2") {
		t.Fatalf("slice 3 before 2: %v", err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdStartSlice, Slice: 2}); err != nil {
		t.Fatal(err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdStartSlice, Slice: 2}); KindOf(err) != KindConflict {
		t.Fatalf("starting slice 2 twice: %v", err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdStartSlice, Slice: 9}); KindOf(err) != KindInvalid {
		t.Fatalf("no slice 9: %v", err)
	}
	s, _ = en.e.Snapshot(s.ID)
	if len(s.Children) != 2 {
		t.Fatalf("children %+v", s.Children)
	}
	c1, _ := en.e.Snapshot(s.Children[0].ID)
	c2, _ := en.e.Snapshot(s.Children[1].ID)
	if c2.Slice.Number != 2 || c2.Base != c1.Branch {
		t.Fatalf("slice 2 should stack on slice 1: %s ← %s (slice 1 is %s)", c2.Branch, c2.Base, c1.Branch)
	}
	if got := sliceStates(en, s.ID); !strings.HasSuffix(got, "3:pending+") {
		t.Fatalf("slice 3 should be startable now: %s", got)
	}
	for _, k := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(gate, "go-"+k), "")
	}
	s = en.waitFor(s.ID, "parent done", func(s *store.RunSnapshot) bool { return s.Status.Terminal() })
	if s.Status != store.StatusDone || len(s.Children) != 3 {
		t.Fatalf("%s %+v", s.Status, s.Children)
	}
	seen := map[int]int{}
	for _, c := range s.Children {
		seen[c.Number]++
	}
	if seen[1] != 1 || seen[2] != 1 || seen[3] != 1 {
		t.Fatalf("each slice should start once: %+v", s.Children)
	}
}

func TestStartSliceEarlyIgnoresSeriesOrder(t *testing.T) {
	en, gate := newEarly(t, false)
	s := en.approveSlices(3)
	en.waitStatus(s.Children[0].ID, store.StatusWaiting)
	if err := en.e.Do(s.ID, Command{Name: CmdStartSlice, Slice: 3}); err != nil {
		t.Fatal(err)
	}
	// Slice 3 finishing first doesn't end the fanout or start slice 2 early.
	writeFile(t, filepath.Join(gate, "go-c"), "")
	en.waitFor(s.ID, "slice 3 done", func(s *store.RunSnapshot) bool {
		return len(s.Children) == 2 && s.Children[1].Status == store.StatusDone
	})
	if s, _ = en.e.Snapshot(s.ID); s.Status != store.StatusFannedOut || len(s.Children) != 2 {
		t.Fatalf("%s %+v", s.Status, s.Children)
	}
	writeFile(t, filepath.Join(gate, "go-a"), "")
	writeFile(t, filepath.Join(gate, "go-b"), "")
	s = en.waitFor(s.ID, "parent done", func(s *store.RunSnapshot) bool { return s.Status.Terminal() })
	if s.Status != store.StatusDone || len(s.Children) != 3 {
		t.Fatalf("%s %+v", s.Status, s.Children)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdStartSlice, Slice: 2}); KindOf(err) != KindConflict {
		t.Fatalf("after the run ended: %v", err)
	}
}
