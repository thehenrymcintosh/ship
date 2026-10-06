package engine

import (
	"fmt"
	"time"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// afterPoll is how often a waiting run re-reads the run it waits on, for
// when that run belongs to another process (a foreground run).
var afterPoll = 10 * time.Second

// waitAfter holds a run created with --after until the run it waits on is
// done, without a workspace or an agent slot. If that run ends any other
// way, it asks whether to start anyway. It returns false when the goroutine
// should exit.
func (r *runner) waitAfter() bool {
	on := r.snap().WaitingOn
	poke := make(chan struct{}, 1)
	unsub := r.e.subscribe(func(id string, ev store.Event) {
		if id == on && ev.Type == store.EvRunFinished {
			select {
			case poke <- struct{}{}:
			default:
			}
		}
	})
	defer unsub()
	tick := time.NewTicker(afterPoll)
	defer tick.Stop()
	for {
		if s := r.snap(); s.PendingAsk == nil {
			other, err := r.e.Snapshot(on)
			switch {
			case err != nil:
				r.askAfter(on + ", which this run waits on, can't be found. Start this run anyway?")
			case other.Status == store.StatusDone:
				if r.noneBusy() != "" {
					break // starts once the main checkout is free (the tick re-checks)
				}
				r.releaseAfter("done")
				return true
			case other.Status.Terminal():
				r.askAfter(fmt.Sprintf("%s (%s), which this run waits on, ended %s. Start this run anyway?", other.Title, on, other.Status))
			}
		}
		select {
		case <-r.ctx.Done():
			return false
		case <-poke:
		case <-tick.C:
		case c := <-r.cmds:
			s := r.snap()
			switch c.Name {
			case CmdStartNow:
				if other := r.noneBusy(); other != "" {
					reply(c, conflict("run %s already uses the main checkout (workspace provider none allows one at a time)", other))
					continue
				}
				r.emitUser(c)
				r.releaseAfter("start_now")
				reply(c, nil)
				return true
			case CmdAnswer:
				if s.PendingAsk == nil || s.PendingAsk.Kind != store.AskKindAfter {
					reply(c, conflict("the run is waiting on %s, not for an answer", on))
					continue
				}
				switch c.Choice {
				case store.AfterStartAnyway:
					if other := r.noneBusy(); other != "" {
						reply(c, conflict("run %s already uses the main checkout (workspace provider none allows one at a time)", other))
						continue
					}
					r.emitUser(c)
					r.releaseAfter(store.AfterStartAnyway)
					reply(c, nil)
					return true
				case store.AfterCancel:
					r.emitUser(c)
					r.emit(store.EvAskAnswered, store.AskAnswered{Choice: store.AfterCancel})
					r.cancelRun()
					reply(c, nil)
					return false
				}
				reply(c, invalid("choose %q or %q", store.AfterStartAnyway, store.AfterCancel))
			case CmdCancel:
				r.emitUser(c)
				r.cancelRun()
				reply(c, nil)
				return false
			case CmdSetVar:
				reply(c, r.setVar(c))
			case CmdPause:
				// Takes effect once the run has started.
				r.requestPause(c)
				reply(c, nil)
			case CmdResume:
				r.requestResume(c)
				reply(c, nil)
			default:
				reply(c, conflict("the run is waiting on %s; start it now or cancel it", on))
			}
		}
	}
}

// noneBusy names another run using the main checkout, when this run
// (workspace provider none) can't start yet because of it.
func (r *runner) noneBusy() string {
	if s := r.snap(); s.Provider == "none" {
		return r.e.activeNoneRun(s.Repo)
	}
	return ""
}

// askAfter asks whether to start a run whose run to wait on didn't finish done.
func (r *runner) askAfter(q string) {
	r.emit(store.EvAskPending, store.AskPending{Kind: store.AskKindAfter, Question: q, Choices: []string{store.AfterStartAnyway, store.AfterCancel}, Input: "none"})
	r.setStatus(store.StatusAsking, "waiting on "+r.snap().WaitingOn+", which didn't finish done")
	r.e.o.Notifier.Notify(brand.Name+": "+r.snap().Title, q)
}

// releaseAfter ends the wait; the run then sets up its workspace from its
// base as it is now (with stack, the other run's branch).
func (r *runner) releaseAfter(how string) {
	s := r.snap()
	if s.PendingAsk != nil {
		r.emit(store.EvAskAnswered, store.AskAnswered{Choice: how})
	}
	rel := store.AfterReleased{How: how}
	if s.AfterStack {
		if other, err := r.e.Snapshot(s.WaitingOn); err == nil && other.Branch != "" {
			rel.Base = other.Branch
		}
	}
	r.emit(store.EvAfterReleased, rel)
	r.setStatus(store.StatusStarting, "")
}
