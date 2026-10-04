package engine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

// With provider auto and treehouse on PATH, runs lease treehouse worktrees:
// returned when a run finishes, kept when it's cancelled so you can take
// over. Skipped when treehouse isn't installed.
func TestAutoProviderUsesTreehouse(t *testing.T) {
	if _, err := exec.LookPath("treehouse"); err != nil {
		t.Skip("treehouse not installed")
	}
	en := newEnv(t, map[string]string{"p": `version: 1
start: work
steps:
  work:
    agent: /work
    next: {done: done}
`})
	t.Setenv("TREEHOUSE_ROOT", filepath.Join(filepath.Dir(en.repo), "pool"))
	en.e = en.newEngine() // picks up TREEHOUSE_ROOT in its base env
	en.cfg.Workspace.Provider = "auto"

	poolStatus := func() []map[string]any {
		cmd := exec.Command("treehouse", "status", "--json")
		cmd.Dir = en.repo
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var list []map[string]any
		json.Unmarshal(out, &list)
		return list
	}

	// A finished run returns its lease.
	s := en.start("p", "", nil, "work: [{outcome: done, summary: ok, run: 'echo x > f && git add -A && git commit -qm work'}]\n")
	if s.Provider != "treehouse" {
		t.Fatalf("auto should pick treehouse, got %q", s.Provider)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	pool := poolStatus()
	if len(pool) == 0 {
		t.Fatal("expected the run's worktree in the treehouse pool")
	}
	for _, w := range pool {
		if w["lease_holder"] == "ship:"+s.ID {
			t.Fatalf("lease not returned: %v", w)
		}
	}
	if gitRun(t, en.repo, "log", "--oneline", "-1", s.Branch) == "" {
		t.Fatal("branch missing")
	}

	// A cancelled run keeps its lease, so you can take over in it.
	s2 := en.start("p", "", nil, "work: [{outcome: done, summary: ok, sleep: 30s}]\n")
	en.waitFor(s2.ID, "working", func(s *store.RunSnapshot) bool { return len(s.Visits) == 1 && !s.Visits[0].Queued })
	if err := en.e.Do(s2.ID, Command{Name: CmdCancel}); err != nil {
		t.Fatal(err)
	}
	s2 = en.waitStatus(s2.ID, store.StatusCancelled)
	if s2.Workspace == nil {
		t.Fatal("cancelled run should keep its worktree")
	}
	if _, err := os.Stat(s2.Workspace.Path); err != nil {
		t.Fatal(err)
	}
	held := false
	for _, w := range poolStatus() {
		held = held || w["lease_holder"] == "ship:"+s2.ID
	}
	if !held {
		t.Fatal("cancelled run's lease should still be held")
	}
}
