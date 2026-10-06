package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thehenrymcintosh/ship/internal/engine/steps"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
	"github.com/thehenrymcintosh/ship/internal/workspace"
)

// fanoutExec runs a child pipeline per slice and supervises the children.
type fanoutExec struct{ r *runner }

// ChildID builds `<parent-id>.<NN>-<key>`.
func ChildID(parent string, number int, key string) string {
	return fmt.Sprintf("%s.%02d-%s", parent, number, key)
}

type childState struct {
	id       string
	snap     *store.RunSnapshot
	advanced bool
}

// Execute implements steps.Executor.
func (f *fanoutExec) Execute(ctx context.Context, v *steps.Visit) (steps.Result, error) {
	r := f.r
	st := v.Step
	// The supervisor owns the run's status while it runs: fanned out, or
	// paused (no new slices). syncStatus is called whenever it wakes.
	syncStatus := func() error {
		if r.paused.Load() {
			return v.RT.SetStatus(store.StatusPaused, "paused: no new slices start; running slices carry on")
		}
		return v.RT.SetStatus(store.StatusFannedOut, "fanout: "+v.StepName)
	}
	if err := syncStatus(); err != nil {
		return steps.Result{}, err
	}
	s := r.snap()
	slices := s.Slices
	if len(slices) == 0 {
		return steps.ErrorResult("no_slices", "fanout needs approved slices, and this run has none"), nil
	}
	series := st.ModeOrDefault() == "series"
	halt := st.OnChildStopOrDefault() == "halt"
	reuse := reusesParent(r.dir, st)

	poke := make(chan struct{}, 1)
	prefix := r.id + "."
	unsub := r.e.subscribe(func(id string, ev store.Event) {
		if strings.HasPrefix(id, prefix) && !strings.Contains(id[len(prefix):], ".") {
			select {
			case poke <- struct{}{}:
			default:
			}
		}
	})
	defer unsub()

	started := map[int]string{} // slice number → child id
	finished := map[string]bool{}
	for _, c := range s.Children {
		started[c.Number] = c.ID
		if c.Status.Terminal() {
			finished[c.ID] = true
		}
	}
	haltedOnce := false
	var startErr error

	// startNow starts slice n out of turn, at a person's request: it skips
	// the series order and max_parallel, but a stacked slice needs the one
	// it stacks on to have a branch.
	startNow := func(n int) error {
		if n < 1 || n > len(slices) {
			return invalid("there's no slice %d (the run has %d)", n, len(slices))
		}
		if _, ok := started[n]; ok {
			return conflict("slice %d has started already", n)
		}
		if haltedOnce || startErr != nil {
			return conflict("the slices have halted, so no more start")
		}
		if why := blockedSlice(n, series, st.Stack, reuse, started); why != "" {
			return conflict("%s", why)
		}
		base := ""
		if series && st.Stack {
			base = s.Base
			if n > 1 {
				prev, err := r.e.Snapshot(started[n-1])
				if err != nil {
					return conflict("slice %d, which slice %d stacks on, can't be read: %v", n-1, n, err)
				}
				base = prev.Branch
			}
		}
		id, err := f.startChild(ctx, v, slices[n-1], len(slices), base, nil)
		if err != nil {
			return invalid("%v", err)
		}
		started[n] = id
		return nil
	}

	for {
		if err := syncStatus(); err != nil {
			return steps.Result{}, err
		}
		// Observe children.
		states := map[int]*childState{}
		running := 0
		for n, id := range started {
			cs, err := r.e.Snapshot(id)
			if err != nil {
				continue
			}
			c := &childState{id: id, snap: cs}
			c.advanced = cs.Status.Terminal() || (st.AdvanceOn != "" && reachedStep(cs, st.AdvanceOn))
			states[n] = c
			if cs.Status.Terminal() {
				if !finished[id] {
					finished[id] = true
					r.emit(store.EvChildFinished, store.ChildFinished{ChildID: id, Status: cs.Status})
				}
			} else {
				running++
			}
		}
		// Halt on the first child that ends without done.
		halted := false
		if halt {
			for _, c := range states {
				if c.snap.Status.Terminal() && c.snap.Status != store.StatusDone {
					halted = true
				}
			}
		}
		if startErr != nil {
			halted = true
		}
		if halted && !haltedOnce {
			haltedOnce = true
			for _, c := range states {
				if !c.snap.Status.Terminal() {
					id := c.id
					go r.e.Do(id, Command{Name: CmdParentHalted, Source: "engine"})
				}
			}
		}

		// Start what can start (unless paused: running slices carry on).
		if !halted && !r.paused.Load() && ctx.Err() == nil {
			if series {
				i := nextUnstarted(started, len(slices))
				if i > 0 {
					prev := states[i-1]
					canStart := i == 1 || (prev != nil && (prev.advanced || (st.AdvanceOn == "" && prev.snap.Status.Terminal())))
					if st.AdvanceOn == "" && prev != nil && !prev.snap.Status.Terminal() {
						canStart = false
					}
					if canStart {
						base := ""
						if st.Stack {
							if i == 1 {
								base = s.Base
							} else if prev != nil {
								base = prev.snap.Branch
							}
						}
						var lease *workspace.Lease
						if reuse {
							lease = s.Workspace
						}
						if id, err := f.startChild(ctx, v, slices[i-1], len(slices), base, lease); err != nil {
							startErr = err
						} else {
							started[i] = id
						}
						continue
					}
				}
			} else {
				launched := false
				for running < st.MaxParallelN() {
					i := nextUnstarted(started, len(slices))
					if i == 0 {
						break
					}
					id, err := f.startChild(ctx, v, slices[i-1], len(slices), "", nil)
					if err != nil {
						startErr = err
						break
					}
					started[i] = id
					running++
					launched = true
				}
				if launched {
					continue
				}
			}
		}

		// Finished?
		if running == 0 && (halted || nextUnstarted(started, len(slices)) == 0) {
			return f.result(slices, states, started, startErr), nil
		}
		select {
		case <-poke:
		case c := <-r.sliceCmds:
			reply(c, startNow(c.Slice))
		case <-r.wake:
		case <-ctx.Done():
			return steps.Result{Outcome: steps.OutcomeCancelled, Summary: "cancelled"}, nil
		}
	}
}

// reusesParent reports whether a fanout's slices work in the parent's
// worktree (series only), so only one can run at a time.
func reusesParent(runDir string, st *pipeline.Step) bool {
	if st.ModeOrDefault() != "series" {
		return false
	}
	cf, _ := pipeline.NewLoader(filepath.Join(runDir, store.PipelineDir)).Load(pipeline.Str(st.Fanout))
	return cf != nil && cf.Pipeline.Workspace != nil && cf.Pipeline.Workspace.Reuse == "parent"
}

// blockedSlice says why slice n can't be started out of turn ("" if it can).
func blockedSlice(n int, series, stack, reuse bool, started map[int]string) string {
	if reuse {
		return "the slices share the parent's worktree, so they run one at a time, in order"
	}
	if series && stack && n > 1 {
		if _, ok := started[n-1]; !ok {
			return fmt.Sprintf("slice %d stacks on slice %d, which hasn't started yet", n, n-1)
		}
	}
	return ""
}

func nextUnstarted(started map[int]string, n int) int {
	for i := 1; i <= n; i++ {
		if _, ok := started[i]; !ok {
			return i
		}
	}
	return 0
}

func reachedStep(s *store.RunSnapshot, step string) bool {
	if step == pipeline.TargetDone {
		return s.Status == store.StatusDone
	}
	for _, v := range s.Visits {
		if v.Step == step {
			return true
		}
	}
	return false
}

// startChild creates (or re-links, after a crash) the child run for a slice.
func (f *fanoutExec) startChild(ctx context.Context, v *steps.Visit, sl store.Slice, count int, base string, reuse *workspace.Lease) (string, error) {
	r := f.r
	id := ChildID(r.id, sl.Number, sl.Key)
	if _, err := r.e.o.Store.Load(id); err != nil {
		data, err := os.ReadFile(sl.File)
		if err != nil {
			return "", fmt.Errorf("slice %s: %w", sl.Key, err)
		}
		parent := r.snap()
		_, err = r.e.Start(ctx, StartRequest{
			Repo: parent.Repo, Pipeline: pipeline.Str(v.Step.Fanout), Brief: data,
			child: &childSpec{parent: parent, parentDir: r.dir, step: v.StepName, slice: sl, count: count, base: base, reuse: reuse, id: id},
		})
		if err != nil {
			msg := err.Error()
			if e, ok := err.(*Error); ok && len(e.Findings) > 0 {
				msg += ": " + e.Findings[0].String()
			}
			return "", fmt.Errorf("child for slice %s didn't start: %s", sl.Key, msg)
		}
	}
	r.emit(store.EvChildStarted, store.ChildStarted{ChildID: id, SliceKey: sl.Key, Number: sl.Number})
	return id, nil
}

func (f *fanoutExec) result(slices []store.Slice, states map[int]*childState, started map[int]string, startErr error) steps.Result {
	var b strings.Builder
	b.WriteString("| # | Slice | Status | Last step | Branch |\n|---|---|---|---|---|\n")
	allDone := true
	for _, sl := range slices {
		c := states[sl.Number]
		status, last, branch := "not started", "", ""
		if c != nil {
			status, last, branch = string(c.snap.Status), c.snap.CurrentStep, c.snap.Branch
			if lv := c.snap.LastVisit(); lv != nil {
				last = lv.Step
			}
		}
		if c == nil || c.snap.Status != store.StatusDone {
			allDone = false
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | `%s` |\n", sl.Number, sl.Title, status, last, branch)
	}
	if startErr != nil {
		b.WriteString("\n" + startErr.Error() + "\n")
	}
	outcome := "failed"
	if allDone {
		outcome = "done"
	}
	return steps.Result{Outcome: outcome, Summary: b.String()}
}

// SliceState is one of a fanout parent's slices, started or not.
type SliceState struct {
	Number int          `json:"number"`
	Key    string       `json:"key"`
	Title  string       `json:"title"`
	RunID  string       `json:"run_id,omitempty"` // once started
	Status store.Status `json:"status"`           // "pending" until started
	Step   string       `json:"step,omitempty"`   // the started slice's current step
	// CanStart: a pending slice can be started now, out of turn; Why says
	// why not otherwise.
	CanStart bool   `json:"can_start"`
	Why      string `json:"why,omitempty"`
}

// SlicePending is the status of a slice that hasn't started.
const SlicePending store.Status = "pending"

// Slices lists a run's approved slices with each one's state.
func (e *Engine) Slices(s *store.RunSnapshot) []SliceState {
	if len(s.Slices) == 0 {
		return nil
	}
	started := map[int]string{}
	for _, c := range s.Children {
		started[c.Number] = c.ID
	}
	// Slices start out of turn only while the fanout step supervises them.
	var st *pipeline.Step
	if lv := s.LastVisit(); lv != nil && lv.Type == pipeline.TypeFanout && lv.Running() && !s.Status.Terminal() {
		if p, err := e.RunPipeline(s.ID); err == nil {
			st = p.Steps[lv.Step]
		}
	}
	out := make([]SliceState, 0, len(s.Slices))
	for _, sl := range s.Slices {
		ss := SliceState{Number: sl.Number, Key: sl.Key, Title: sl.Title, Status: SlicePending}
		if id, ok := started[sl.Number]; ok {
			ss.RunID, ss.Status = id, store.StatusStarting
			if cs, err := e.Snapshot(id); err == nil {
				ss.Status, ss.Step = cs.Status, cs.CurrentStep
			}
		} else if st == nil {
			ss.Why = "slices start while the run is at its fanout step"
		} else {
			series := st.ModeOrDefault() == "series"
			ss.Why = blockedSlice(sl.Number, series, st.Stack, reusesParent(e.o.Store.RunDir(s.ID), st), started)
			ss.CanStart = ss.Why == ""
		}
		out = append(out, ss)
	}
	return out
}
