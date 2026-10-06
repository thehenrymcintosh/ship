package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
)

// restart shuts the engine down (as on SIGTERM) and starts a new one that
// recovers from disk.
func (en *env) restart() {
	en.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := en.e.Shutdown(ctx); err != nil {
		en.t.Fatal(err)
	}
	en.e = en.newEngine()
	if _, err := en.e.Recover(); err != nil {
		en.t.Fatal(err)
	}
}

func TestRecoverInterruptedAgent(t *testing.T) {
	en := newEnv(t, map[string]string{"feat": agentPipeline})
	s := en.start("feat", "", nil, `implement:
  - {outcome: done, summary: first, sleep: 30s}
  - {outcome: done, summary: resumed}
review: [{outcome: pass, summary: ok}]
`)
	en.waitFor(s.ID, "implement running", func(s *store.RunSnapshot) bool {
		return len(s.Visits) == 1 && !s.Visits[0].Queued
	})
	en.restart()
	s = en.waitStatus(s.ID, store.StatusNeedsAttention)
	lv := s.LastVisit()
	if !lv.Interrupted || lv.SessionID == "" {
		t.Fatalf("%+v", lv)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdResumeSession}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	if s.Visits[1].ResumeID != s.Visits[0].SessionID || s.Visits[1].Step != "implement" {
		t.Fatalf("resume visit %+v", s.Visits[1])
	}
}

// An agent waiting out a usage limit is "waiting" with its visit still
// running. If ship dies without shutting down, recovery must park it like
// any interrupted agent, not re-run the step in a new conversation.
func TestRecoverAgentWaitingForLimitAfterCrash(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: work
steps:
  work: {prompt: Do it., next: done}
`})
	s := en.start("p", "", nil, `work: [{outcome: done, summary: ok, limited: 1, limit_reset: 1h}]`)
	en.waitStatus(s.ID, store.StatusWaiting)
	// Keep the run's state as it was before the graceful shutdown marks the
	// visit interrupted, then put it back: that's what a crash leaves.
	dir := en.e.o.Store.RunDir(s.ID)
	saved := filepath.Join(t.TempDir(), "run")
	if err := os.CopyFS(saved, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := en.e.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(dir)
	if err := os.CopyFS(dir, os.DirFS(saved)); err != nil {
		t.Fatal(err)
	}
	en.e = en.newEngine()
	if _, err := en.e.Recover(); err != nil {
		t.Fatal(err)
	}
	checkCarriesOn(t, en, s.ID)
}

// checkCarriesOn checks a run cut off during a usage-limit wait carries on
// by itself in the same conversation (and here, waits for the limit again).
func checkCarriesOn(t *testing.T, en *env, id string) {
	t.Helper()
	s := en.waitFor(id, "the step to carry on", func(s *store.RunSnapshot) bool {
		return len(s.Visits) == 2 && s.Status == store.StatusWaiting
	})
	first, second := s.Visits[0], s.Visits[1]
	if !first.Interrupted || first.SessionID == "" || second.ResumeID != first.SessionID {
		t.Fatalf("%s first %+v second %+v", visitTrail(s), first, second)
	}
}

func TestLimitWaitDoesntHoldUpARestart(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: work
steps:
  work: {prompt: Do it., next: done}
`})
	s := en.start("p", "", nil, `work: [{outcome: done, summary: ok, limited: 1, limit_reset: 1h}]`)
	en.waitStatus(s.ID, store.StatusWaiting)
	if n := en.e.Executing(); n != 0 {
		t.Fatalf("a limit wait counts as executing (%d)", n)
	}
	en.restart()
	checkCarriesOn(t, en, s.ID)
}

func TestRecoverAskAndWait(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: check-in
workspace: {provider: none}
steps:
  check-in:
    ask: go?
    choices: {go: poll}
  poll:
    wait: test -f ready && echo ready || echo waiting
    every: 100ms
    timeout: 1m
    next: {ready: done}
`})
	s := en.start("p", "", nil, "")
	en.waitStatus(s.ID, store.StatusAsking)
	en.restart()
	s = en.waitStatus(s.ID, store.StatusAsking)
	if s.PendingAsk == nil || s.PendingAsk.Question != "go?" || len(s.Visits) != 1 {
		t.Fatalf("ask not restored: %+v", s.PendingAsk)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "go"}); err != nil {
		t.Fatal(err)
	}
	en.waitFor(s.ID, "a few polls", func(s *store.RunSnapshot) bool {
		lv := s.LastVisit()
		return lv != nil && lv.Step == "poll" && lv.Polls >= 2
	})
	en.restart()
	s = en.waitFor(s.ID, "wait resumed", func(s *store.RunSnapshot) bool { return s.Status == store.StatusWaiting && s.LastVisit().Polls >= 3 })
	writeFile(t, en.repo+"/ready", "")
	s = en.waitStatus(s.ID, store.StatusDone)
	if len(s.Visits) != 2 || s.Visits[1].Outcome != "ready" {
		t.Fatalf("one wait visit expected: %s", visitTrail(s))
	}
}

func TestRecoverWorktreeMissing(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: check-in
steps:
  check-in:
    ask: go?
    choices: {go: done}
`})
	s := en.start("p", "", nil, "")
	s = en.waitStatus(s.ID, store.StatusAsking)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	en.e.Shutdown(ctx)
	cancel()
	gitRun(t, en.repo, "worktree", "remove", "--force", s.Workspace.Path)
	en.e = en.newEngine()
	en.e.Recover()
	s = en.waitStatus(s.ID, store.StatusNeedsAttention)
	if !s.WorkspaceMissing {
		t.Fatal("want worktree_missing")
	}
	if err := en.e.Do(s.ID, Command{Name: CmdReacquire}); err != nil {
		t.Fatal(err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdGoto, Step: "check-in"}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusAsking)
	if s.WorkspaceMissing || s.Workspace == nil {
		t.Fatal("re-acquire should restore the worktree")
	}
}
