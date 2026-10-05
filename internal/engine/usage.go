package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// Usage is where the Claude account stands against its usage limits, as the
// CLI last reported it during any agent step. It covers everything on the
// account, including Claude use outside ship.
type Usage struct {
	Windows   []agent.LimitWindow `json:"windows"`
	Rejected  bool                `json:"rejected"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// windowLengths are the known windows' spans, to find where one started.
var windowLengths = map[string]time.Duration{
	"five_hour": 5 * time.Hour,
	"seven_day": 7 * 24 * time.Hour,
}

// WindowLabel names a window for people.
func WindowLabel(name string) string {
	switch name {
	case "five_hour":
		return "5-hour"
	case "seven_day":
		return "weekly"
	case "seven_day_opus":
		return "weekly Opus"
	}
	return name
}

// Start returns when the window began, if its length is known.
func WindowStart(w agent.LimitWindow) (time.Time, bool) {
	d, ok := windowLengths[w.Name]
	if !ok || w.ResetsAt.IsZero() {
		return time.Time{}, false
	}
	return w.ResetsAt.Add(-d), true
}

func (e *Engine) usagePath() string { return filepath.Join(e.o.Store.Root, "usage.json") }

// observeUsage records a report from an agent.
func (e *Engine) observeUsage(rep agent.UsageReport) {
	e.umu.Lock()
	defer e.umu.Unlock()
	if e.usage == nil {
		e.usage = e.loadUsage()
	}
	u := e.usage
	for _, w := range rep.Windows {
		found := false
		for i := range u.Windows {
			if u.Windows[i].Name == w.Name {
				u.Windows[i], found = w, true
			}
		}
		if !found {
			u.Windows = append(u.Windows, w)
		}
	}
	sort.Slice(u.Windows, func(i, j int) bool {
		return windowLengths[u.Windows[i].Name] < windowLengths[u.Windows[j].Name]
	})
	u.Rejected, u.UpdatedAt = rep.Rejected, time.Now()
	// Persist at most every few seconds; reports come with every request.
	if time.Since(e.usageSaved) > 5*time.Second || rep.Rejected {
		e.usageSaved = time.Now()
		if b, err := json.Marshal(u); err == nil {
			_ = store.WriteFileAtomic(e.usagePath(), b, 0o600)
		}
	}
}

func (e *Engine) loadUsage() *Usage {
	u := &Usage{}
	if b, err := os.ReadFile(e.usagePath()); err == nil {
		_ = json.Unmarshal(b, u)
	}
	return u
}

// Usage returns the latest usage. A window that has reset since is shown
// empty, with its next reset unknown until an agent reports again.
func (e *Engine) Usage() Usage {
	e.umu.Lock()
	if e.usage == nil {
		e.usage = e.loadUsage()
	}
	u := *e.usage
	u.Windows = append([]agent.LimitWindow(nil), e.usage.Windows...)
	e.umu.Unlock()
	now := time.Now()
	for i, w := range u.Windows {
		if !w.ResetsAt.IsZero() && now.After(w.ResetsAt) {
			u.Windows[i].Utilization, u.Windows[i].ResetsAt = 0, time.Time{}
			u.Rejected = false
		}
	}
	return u
}

// Focus pauses every other active top-level run, slices included, so the
// usage left goes to finishing this one. Each pauses after its current
// step. Runs waiting in the inbox aren't spending usage, so they're left
// alone. It returns the runs it paused, and one message per run it couldn't
// pause; those don't stop it pausing the rest.
func (e *Engine) Focus(id, source string) (paused, failed []string, err error) {
	snap, err := e.Snapshot(id)
	if err != nil {
		return nil, nil, &Error{Kind: KindNotFound, Msg: "run " + id + " not found"}
	}
	root := snap
	for root.Parent != nil {
		p, err := e.Snapshot(root.Parent.ID)
		if err != nil {
			break
		}
		root = p
	}
	e.mu.Lock()
	var ids []string
	for rid := range e.runners {
		ids = append(ids, rid)
	}
	e.mu.Unlock()
	sort.Strings(ids)
	for _, rid := range ids {
		s, err := e.Snapshot(rid)
		if err != nil || s.Parent != nil || rid == root.ID || s.Status.Terminal() || s.Status == store.StatusPaused ||
			s.Status == store.StatusNeedsAttention || s.PauseRequested {
			continue
		}
		if err := e.Do(rid, Command{Name: CmdPause, All: true, Source: source}); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", rid, err))
			continue
		}
		paused = append(paused, rid)
	}
	return paused, failed, nil
}
