package engine

import (
	"context"
	"testing"
	"time"

	"github.com/merlin-digital/ship/internal/store"
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

func TestRecoverAskAndWait(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: chef
workspace: {provider: none}
steps:
  chef:
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
start: chef
steps:
  chef:
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
	if err := en.e.Do(s.ID, Command{Name: CmdGoto, Step: "chef"}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusAsking)
	if s.WorkspaceMissing || s.Workspace == nil {
		t.Fatal("re-acquire should restore the worktree")
	}
}
