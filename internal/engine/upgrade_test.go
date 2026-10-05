package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

func TestUpgradeRunOntoEditedPipeline(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: a
workspace: {provider: none}
steps:
  a: {run: "sleep 5", timeout: 1s, next: done}
`})
	s := en.start("p", "", nil, "")
	s = en.waitStatus(s.ID, store.StatusNeedsAttention)
	if live, err := en.e.LivePipeline(s); err != nil || live.Changed {
		t.Fatalf("unedited pipeline: %v %+v", err, live)
	}

	// The fix: a passes now and a new step follows it.
	fixed := `version: 1
start: a
workspace: {provider: none}
steps:
  a: {run: "true", next: b}
  b: {run: "true", next: done}
`
	file := filepath.Join(en.repo, ".ship", "pipelines", "p.yml")
	writeFile(t, file, "version: 1\nstart: nope\nsteps: {}\n")
	if _, err := en.e.Upgrade(context.Background(), s.ID, "a", "cli"); KindOf(err) != KindInvalid {
		t.Fatalf("upgrade onto a broken pipeline: %v", err)
	}
	writeFile(t, file, fixed)
	if live, err := en.e.LivePipeline(s); err != nil || !live.Changed {
		t.Fatalf("edited pipeline: %v %+v", err, live)
	}
	if _, err := en.e.Upgrade(context.Background(), s.ID, "c", "cli"); KindOf(err) != KindInvalid || !strings.Contains(err.Error(), "a, b") {
		t.Fatalf("upgrade to a missing step: %v", err)
	}
	if _, err := os.Stat(filepath.Join(en.e.o.Store.RunDir(s.ID), store.PipelineDir+".next")); !os.IsNotExist(err) {
		t.Fatalf("staged copy left behind: %v", err)
	}

	// The current step (a) is the default.
	if _, err := en.e.Upgrade(context.Background(), s.ID, "", "cli"); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "a:error a:pass b:pass" || s.Upgrades != 1 {
		t.Fatalf("trail %q upgrades %d", got, s.Upgrades)
	}
	snapCopy, _ := os.ReadFile(filepath.Join(en.e.o.Store.RunDir(s.ID), store.PipelineDir, "p.yml"))
	if string(snapCopy) != fixed {
		t.Fatalf("run's copy wasn't replaced:\n%s", snapCopy)
	}
	// Replaying the log gives the same run.
	if loaded, err := en.e.o.Store.Load(s.ID); err != nil || loaded.Upgrades != 1 {
		t.Fatalf("reload: %v %+v", err, loaded)
	}
}

func TestUpgradeResumesAPausedRunAtTheChosenStep(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: a
workspace: {provider: none}
steps:
  a: {run: "sleep 1", next: b}
  b: {run: "exit 1", next: done}
`})
	s := en.start("p", "", nil, "")
	en.waitFor(s.ID, "a running", func(s *store.RunSnapshot) bool { return len(s.Visits) == 1 })
	if err := en.e.Do(s.ID, Command{Name: CmdPause}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusPaused)
	writeFile(t, filepath.Join(en.repo, ".ship", "pipelines", "p.yml"), `version: 1
start: a
workspace: {provider: none}
steps:
  a: {run: "true", next: b2}
  b2: {run: "true", next: done}
`)
	if _, err := en.e.Upgrade(context.Background(), s.ID, "b2", "ui"); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "a:pass b2:pass" || s.PauseRequested {
		t.Fatalf("trail %q", got)
	}
}

func TestUpgradeStopsTheRunningStep(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: a
workspace: {provider: none}
steps:
  a: {run: "sleep 30", next: done}
`})
	s := en.start("p", "", nil, "")
	en.waitFor(s.ID, "a running", func(s *store.RunSnapshot) bool { return len(s.Visits) == 1 })
	writeFile(t, filepath.Join(en.repo, ".ship", "pipelines", "p.yml"), `version: 1
start: a
workspace: {provider: none}
steps:
  a: {run: "true", next: done}
`)
	if _, err := en.e.Upgrade(context.Background(), s.ID, "a", "cli"); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "a:cancelled a:pass" {
		t.Fatalf("trail %q", got)
	}
}
