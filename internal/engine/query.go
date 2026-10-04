package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/merlin-digital/ship/internal/pipeline"
	"github.com/merlin-digital/ship/internal/store"
	"github.com/merlin-digital/ship/internal/workspace"
)

// RunPipeline loads a run's pipeline snapshot.
func (e *Engine) RunPipeline(id string) (*pipeline.Pipeline, error) {
	snap, err := e.Snapshot(id)
	if err != nil {
		return nil, err
	}
	f, findings := pipeline.NewLoader(filepath.Join(e.o.Store.RunDir(id), store.PipelineDir)).Load(snap.Pipeline)
	if f == nil {
		return nil, fmt.Errorf("pipeline snapshot: %v", findings)
	}
	return f.Pipeline, nil
}

// Graph returns a run's pipeline graph with visit counts, the current node
// and taken edges.
func (e *Engine) Graph(id string) (pipeline.Graph, error) {
	p, err := e.RunPipeline(id)
	if err != nil {
		return pipeline.Graph{}, err
	}
	snap, _ := e.Snapshot(id)
	g := p.Graph()
	for i := range g.Nodes {
		n := &g.Nodes[i]
		n.Visits = snap.VisitTotals[n.ID]
		n.Current = !snap.Status.Terminal() && snap.CurrentStep == n.ID
		if snap.Status == store.StatusDone && n.ID == pipeline.TargetDone || snap.Status == store.StatusStopped && n.ID == pipeline.TargetStop {
			n.Current = true
		}
	}
	taken := map[[2]string]bool{}
	evs, _, _ := store.ReadEvents(e.o.Store.RunDir(id))
	for _, ev := range evs {
		if ev.Type != store.EvTransition {
			continue
		}
		var t store.Transition
		if json.Unmarshal(ev.Data, &t) == nil {
			taken[[2]string{t.From, t.To}] = true
		}
	}
	for i := range g.Edges {
		g.Edges[i].Taken = taken[[2]string{g.Edges[i].From, g.Edges[i].To}]
	}
	return g, nil
}

// InboxItem is one run waiting for a human.
type InboxItem struct {
	Run      *store.RunSnapshot `json:"run"`
	Kind     string             `json:"kind"` // ask | split_review | var | needs_attention
	Question string             `json:"question"`
	Since    time.Time          `json:"since"`
}

// Inbox lists runs waiting for a human, oldest first.
func (e *Engine) Inbox() ([]InboxItem, error) {
	snaps, err := e.o.Store.List()
	if err != nil {
		return nil, err
	}
	var out []InboxItem
	for _, s := range snaps {
		if live, err := e.Snapshot(s.ID); err == nil {
			s = live
		}
		switch {
		case s.Status == store.StatusAsking && s.PendingAsk != nil:
			out = append(out, InboxItem{Run: s, Kind: s.PendingAsk.Kind, Question: s.PendingAsk.Question, Since: s.PendingAsk.Since})
		case s.Status == store.StatusNeedsAttention:
			out = append(out, InboxItem{Run: s, Kind: "needs_attention", Question: s.StatusReason, Since: s.UpdatedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out, nil
}

// CleanOptions controls Clean.
type CleanOptions struct {
	RunID    string
	Force    bool
	DryRun   bool
	KeepDays int
}

// CleanResult reports what Clean did (or would do).
type CleanResult struct {
	Released []string `json:"released"`
	Deleted  []string `json:"deleted"`
	Errors   []string `json:"errors,omitempty"`
}

// Clean releases worktrees kept by terminal runs and deletes run dirs older
// than KeepDays whose worktrees are gone.
func (e *Engine) Clean(ctx context.Context, o CleanOptions) (CleanResult, error) {
	var res CleanResult
	ids, err := e.o.Store.IDs()
	if err != nil {
		return res, err
	}
	if o.RunID != "" {
		ids = []string{o.RunID}
	}
	for _, id := range ids {
		if e.Active(id) {
			continue
		}
		snap, err := e.o.Store.Load(id)
		if err != nil || !snap.Status.Terminal() {
			continue
		}
		if snap.Workspace != nil && snap.Provider != parentProvider && snap.Provider != "none" {
			if o.DryRun {
				res.Released = append(res.Released, id)
			} else if err := e.releaseTerminal(ctx, id, snap, o.Force); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", id, err))
			} else {
				res.Released = append(res.Released, id)
				snap.Workspace = nil
			}
		}
		if o.RunID == "" && o.KeepDays > 0 && snap.Workspace == nil && snap.FinishedAt != nil &&
			time.Since(*snap.FinishedAt) > time.Duration(o.KeepDays)*24*time.Hour {
			if !o.DryRun {
				if err := os.RemoveAll(e.o.Store.RunDir(id)); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", id, err))
					continue
				}
			}
			res.Deleted = append(res.Deleted, id)
		}
	}
	return res, nil
}

func (e *Engine) releaseTerminal(ctx context.Context, id string, snap *store.RunSnapshot, force bool) error {
	cfg, err := e.o.LoadConfig(snap.Repo)
	if err != nil {
		return err
	}
	prov, err := e.o.Providers(snap.Provider, cfg)
	if err != nil {
		return err
	}
	if err := prov.Release(ctx, *snap.Workspace, workspace.ReleaseOpts{Force: force}); err != nil {
		return err
	}
	rl, err := e.o.Store.Open(id)
	if err != nil {
		return err
	}
	defer rl.Close()
	rl.OnEvent = func(ev store.Event, s *store.RunSnapshot) { e.dispatch(id, ev, s) }
	_, err = rl.Emit(store.EvWorkspaceReleased, store.ActorUser, store.WorkspaceReleased{Forced: force})
	return err
}
