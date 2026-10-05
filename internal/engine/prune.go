package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
)

// PruneOptions controls Prune.
type PruneOptions struct {
	OlderThan time.Duration // finished at least this long ago
	Force     bool          // also runs that still have a worktree (it's removed)
	DryRun    bool
}

// PrunedRun is one run whose state was (or would be) deleted.
type PrunedRun struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Status   string    `json:"status"`
	Finished time.Time `json:"finished"`
	Bytes    int64     `json:"bytes"`    // with its slices
	Slices   int       `json:"slices"`   // child runs deleted with it
	Worktree string    `json:"worktree"` // removed too (with Force)
}

// PruneResult reports what Prune did (or would do).
type PruneResult struct {
	Pruned  []PrunedRun `json:"pruned"`
	Bytes   int64       `json:"bytes"`
	Skipped []string    `json:"skipped,omitempty"` // old enough, but kept, and why
	Errors  []string    `json:"errors,omitempty"`
	Note    string      `json:"note,omitempty"` // why nothing was looked at
}

// Prune deletes the state of finished runs older than OlderThan. A run's
// slices go with it, and only when they've all finished too. Runs that still
// have a worktree are kept unless Force, which removes the worktree first.
// Pipeline history lives in the repo, so it's untouched.
func (e *Engine) Prune(ctx context.Context, o PruneOptions) (PruneResult, error) {
	var res PruneResult
	ids, err := e.o.Store.IDs()
	if err != nil {
		return res, err
	}
	snaps := map[string]*store.RunSnapshot{}
	for _, id := range ids {
		if s, err := e.o.Store.Load(id); err == nil {
			snaps[id] = s
		}
	}
	// Group each run under its top-level run (a slice whose parent is gone
	// is its own group).
	groups := map[string][]*store.RunSnapshot{}
	for _, s := range snaps {
		root := s
		for root.Parent != nil && snaps[root.Parent.ID] != nil {
			root = snaps[root.Parent.ID]
		}
		groups[root.ID] = append(groups[root.ID], s)
	}
	roots := make([]string, 0, len(groups))
	for id := range groups {
		roots = append(roots, id)
	}
	sort.Strings(roots)

	cutoff := time.Now().Add(-o.OlderThan)
	for _, id := range roots {
		group := groups[id]
		root := snaps[id]
		var finished time.Time
		ok := true
		for _, s := range group {
			if e.Active(s.ID) || !s.Status.Terminal() || s.FinishedAt == nil {
				ok = false
				break
			}
			if s.FinishedAt.After(finished) {
				finished = *s.FinishedAt
			}
		}
		if !ok || finished.After(cutoff) {
			continue
		}
		var worktrees []*store.RunSnapshot
		for _, s := range group {
			if hasWorktree(s) {
				worktrees = append(worktrees, s)
			}
		}
		pr := PrunedRun{ID: id, Title: root.Title, Status: string(root.Status), Finished: finished, Slices: len(group) - 1}
		if len(worktrees) > 0 {
			if !o.Force {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: its worktree is still at %s (ship clean --run %s removes it; or prune --force)", id, worktrees[0].Workspace.Path, worktrees[0].ID))
				continue
			}
			pr.Worktree = worktrees[0].Workspace.Path
		}
		for _, s := range group {
			pr.Bytes += dirSize(e.o.Store.RunDir(s.ID))
		}
		if !o.DryRun {
			failed := false
			for _, s := range worktrees {
				if err := e.releaseTerminal(ctx, s.ID, s, true); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", s.ID, err))
					failed = true
				}
			}
			if failed {
				continue
			}
			for _, s := range group {
				if err := os.RemoveAll(e.o.Store.RunDir(s.ID)); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", s.ID, err))
					failed = true
				}
			}
			if failed {
				continue
			}
		}
		res.Pruned = append(res.Pruned, pr)
		res.Bytes += pr.Bytes
	}
	return res, nil
}

// hasWorktree reports whether the run holds a worktree that's still on disk.
func hasWorktree(s *store.RunSnapshot) bool {
	if s.Workspace == nil || s.Provider == parentProvider || s.Provider == "none" {
		return false
	}
	_, err := os.Stat(s.Workspace.Path)
	return err == nil
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
