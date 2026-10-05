package engine

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
)

func TestPrune(t *testing.T) {
	ask := "version: 1\nstart: q\nsteps:\n  q: {ask: Go?, choices: {yes: done}}\n"
	en := newEnv(t, map[string]string{"parent": ticketParent, "child": ticketChild, "ask": ask})
	split := `split:
  - outcome: ok
    summary: two
    slices:
      - {key: a, title: A, brief: a, acceptance: [x]}
      - {key: b, title: B, brief: b, acceptance: [x]}
`
	done := en.start("parent", "", map[string]string{"ticket": "API-1"}, split)
	done = en.waitStatus(done.ID, store.StatusDone)
	waiting := en.waitStatus(en.start("ask", "", nil, "").ID, store.StatusAsking)
	kept := en.waitStatus(en.start("ask", "", nil, "").ID, store.StatusAsking)
	if err := en.e.Do(kept.ID, Command{Name: CmdCancel}); err != nil {
		t.Fatal(err)
	}
	kept = en.waitFor(kept.ID, "stopped", func(s *store.RunSnapshot) bool { return s.Status.Terminal() && !en.e.Active(s.ID) })
	if kept.Workspace == nil {
		t.Fatal("a cancelled run should keep its worktree")
	}
	ids := []string{done.ID}
	for _, c := range done.Children {
		ids = append(ids, c.ID)
	}
	for _, id := range ids {
		en.waitFor(id, "settled", func(s *store.RunSnapshot) bool { return !en.e.Active(s.ID) })
	}
	ctx := context.Background()
	exists := func(id string) bool {
		_, err := os.Stat(en.st.RunDir(id))
		return err == nil
	}

	// Nothing has been finished an hour.
	res, err := en.e.Prune(ctx, PruneOptions{OlderThan: time.Hour})
	if err != nil || len(res.Pruned) != 0 {
		t.Fatalf("%v %+v", err, res)
	}

	res, err = en.e.Prune(ctx, PruneOptions{DryRun: true})
	if err != nil || len(res.Pruned) != 1 || res.Pruned[0].ID != done.ID || res.Pruned[0].Slices != 2 || res.Bytes == 0 || len(res.Skipped) != 1 {
		t.Fatalf("dry run: %v %+v", err, res)
	}
	if !exists(done.ID) {
		t.Fatal("a dry run deleted something")
	}

	res, err = en.e.Prune(ctx, PruneOptions{})
	if err != nil || len(res.Pruned) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if exists(done.ID) || !exists(waiting.ID) || !exists(kept.ID) {
		t.Fatal("pruned the wrong runs")
	}
	for _, c := range done.Children {
		if exists(c.ID) {
			t.Fatalf("slice %s outlived its run", c.ID)
		}
	}

	// --force takes the worktree with it.
	res, err = en.e.Prune(ctx, PruneOptions{Force: true})
	if err != nil || len(res.Pruned) != 1 || res.Pruned[0].Worktree == "" || len(res.Errors) != 0 {
		t.Fatalf("force: %v %+v", err, res)
	}
	if exists(kept.ID) || !exists(waiting.ID) {
		t.Fatal("force pruned the wrong runs")
	}
	if _, err := os.Stat(kept.Workspace.Path); err == nil {
		t.Fatal("the worktree is still there")
	}
}
