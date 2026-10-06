package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
	"github.com/thehenrymcintosh/ship/internal/workspace"
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
	history := map[string][]pipeline.GraphVisit{}
	for i := range snap.Visits {
		v := &snap.Visits[i]
		oc := v.Outcome
		if oc == "" && v.Interrupted {
			oc = "interrupted"
		}
		history[v.Step] = append(history[v.Step], pipeline.GraphVisit{
			Seq: v.Seq, Number: v.VisitNumber, Outcome: oc, Running: v.Running(),
			DurationMS: v.DurationMS, CostUSD: v.CostUSD, Summary: v.Summary,
		})
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		n.History = history[n.ID]
		n.Visits = snap.VisitTotals[n.ID]
		n.Current = !snap.Status.Terminal() && snap.CurrentStep == n.ID
		if snap.Status == store.StatusDone && n.ID == pipeline.TargetDone || snap.Status == store.StatusStopped && n.ID == pipeline.TargetStop {
			n.Current = true
		}
	}
	evs, _, _ := store.ReadEvents(e.o.Store.RunDir(id))
	markTaken(&g, evs)
	return g, nil
}

// markTaken marks the edges the run's transitions took. A step can reach
// the same next step by several outcomes (major, minor, patch…), so an edge
// matches on its label too: the outcome (error for error routes), or
// exhausted. A transition no edge is labelled for (a manual goto, say) marks
// every edge between its steps.
func markTaken(g *pipeline.Graph, evs []store.Event) {
	for _, ev := range evs {
		if ev.Type != store.EvTransition {
			continue
		}
		var t store.Transition
		if json.Unmarshal(ev.Data, &t) != nil {
			continue
		}
		label := t.Outcome
		if t.Reason == store.ReasonExhausted {
			label = "exhausted"
		}
		matched := false
		for i := range g.Edges {
			if ed := &g.Edges[i]; ed.From == t.From && ed.To == t.To && ed.Label == label {
				ed.Taken, matched = true, true
			}
		}
		if !matched {
			for i := range g.Edges {
				if ed := &g.Edges[i]; ed.From == t.From && ed.To == t.To {
					ed.Taken = true
				}
			}
		}
	}
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
	RunID  string
	Force  bool
	DryRun bool
}

// CleanResult reports what Clean did (or would do).
type CleanResult struct {
	Released []string `json:"released"`
	Errors   []string `json:"errors,omitempty"`
}

// Clean releases worktrees kept by terminal runs. Prune deletes their state.
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
