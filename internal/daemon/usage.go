package daemon

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// WindowView is one usage-limit window for the UI.
type WindowView struct {
	Name     string    `json:"name"`
	Label    string    `json:"label"`
	Pct      int       `json:"pct"`
	ResetsAt time.Time `json:"resets_at,omitempty"`
	Level    string    `json:"level"` // ok | warn | danger
}

// RunUsage is what one top-level run (with its slices) has spent in the
// current 5-hour window.
type RunUsage struct {
	ID     string       `json:"id"`
	Title  string       `json:"title"`
	Status store.Status `json:"status"`
	// StatusReason is for the status pill.
	StatusReason string  `json:"status_reason,omitempty"`
	USD          float64 `json:"usd"`
	Pct          int     `json:"pct"` // estimated share of the window
	Working      bool    `json:"working"`
	Paused       bool    `json:"paused"`
}

// UsageView is the account's usage plus how ship's runs contributed.
type UsageView struct {
	Known     bool         `json:"known"`
	Rejected  bool         `json:"rejected"`
	UpdatedAt time.Time    `json:"updated_at,omitempty"`
	Windows   []WindowView `json:"windows"`
	Runs      []RunUsage   `json:"runs"`
	Advice    string       `json:"advice,omitempty"`
}

// Short is the window that matters most right now (the 5-hour one).
func (u UsageView) Short() *WindowView {
	for i := range u.Windows {
		if u.Windows[i].Name == "five_hour" {
			return &u.Windows[i]
		}
	}
	if len(u.Windows) > 0 {
		return &u.Windows[0]
	}
	return nil
}

func level(pct int, rejected bool) string {
	switch {
	case rejected || pct >= 95:
		return "danger"
	case pct >= 75:
		return "warn"
	}
	return "ok"
}

func (d *Daemon) usageView() UsageView {
	u := d.eng.Usage()
	v := UsageView{Known: len(u.Windows) > 0, Rejected: u.Rejected, UpdatedAt: u.UpdatedAt}
	var short float64
	var since time.Time
	for _, w := range u.Windows {
		pct := int(math.Round(w.Utilization * 100))
		v.Windows = append(v.Windows, WindowView{Name: w.Name, Label: engine.WindowLabel(w.Name), Pct: pct, ResetsAt: w.ResetsAt, Level: level(pct, u.Rejected && w.Utilization >= 1)})
		if w.Name == "five_hour" {
			short = w.Utilization
			since, _ = engine.WindowStart(w)
		}
	}

	// Spend per top-level run in the window, slices counted in their run.
	runs, _ := d.runList("", "", "", 0)
	byID := map[string]*store.RunSnapshot{}
	for _, s := range runs {
		byID[s.ID] = s
	}
	rootOf := func(s *store.RunSnapshot) *store.RunSnapshot {
		for s.Parent != nil && byID[s.Parent.ID] != nil {
			s = byID[s.Parent.ID]
		}
		return s
	}
	spent := map[string]float64{}
	working := map[string]bool{}
	var total float64
	for _, s := range runs {
		root := rootOf(s)
		for _, vs := range s.Visits {
			if vs.Finished != nil && !since.IsZero() && vs.Finished.After(since) {
				spent[root.ID] += vs.CostUSD
				total += vs.CostUSD
			}
			if vs.Running() && !s.Status.Terminal() {
				working[root.ID] = true
			}
		}
	}
	active := 0
	for _, s := range runs {
		if s.Parent != nil || (s.Status.Terminal() && spent[s.ID] == 0) {
			continue
		}
		ru := RunUsage{ID: s.ID, Title: s.Title, Status: s.Status, StatusReason: s.StatusReason, USD: spent[s.ID], Working: working[s.ID],
			Paused: s.Status == store.StatusPaused || s.PauseRequested}
		if total > 0 {
			// The window counts all use of the account; share it out by
			// what each run spent, which assumes ship did most of it.
			ru.Pct = int(math.Round(short * 100 * ru.USD / total))
		}
		if !s.Status.Terminal() && !ru.Paused {
			active++
		}
		v.Runs = append(v.Runs, ru)
	}
	sort.SliceStable(v.Runs, func(i, j int) bool {
		ti, tj := v.Runs[i].Status.Terminal(), v.Runs[j].Status.Terminal()
		if ti != tj {
			return !ti
		}
		return v.Runs[i].USD > v.Runs[j].USD
	})
	if w := v.Short(); w != nil && active >= 2 && (w.Level != "ok") {
		v.Advice = fmt.Sprintf("You've used %d%% of your 5-hour limit with %d runs going. If they all carry on, they may all stop halfway. Focus on the one closest to done: the others pause after their current step, and you resume them later.", w.Pct, active)
	}
	return v
}

// runWindow is a run's spend in the 5-hour window; it reads only the run and
// its slices, so the run page can show it on every refresh.
func (d *Daemon) runWindow(s *store.RunSnapshot, kids []*store.RunSnapshot) *RunWindow {
	for _, w := range d.eng.Usage().Windows {
		if w.Name != "five_hour" {
			continue
		}
		since, ok := engine.WindowStart(w)
		if !ok {
			return nil
		}
		pct := int(math.Round(w.Utilization * 100))
		rw := &RunWindow{Pct: pct, Level: level(pct, false), Resets: w.ResetsAt}
		for _, r := range append([]*store.RunSnapshot{s}, kids...) {
			for _, vs := range r.Visits {
				if vs.Finished != nil && vs.Finished.After(since) {
					rw.USD += vs.CostUSD
				}
			}
		}
		return rw
	}
	return nil
}

func (d *Daemon) getUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, d.usageView())
}

func (d *Daemon) fragUsage(w http.ResponseWriter, r *http.Request) {
	name := "usage-card"
	if r.URL.Query().Get("compact") != "" {
		name = "usage-meter"
	}
	d.render(w, "runs", name, d.usageView())
}

// focus pauses every other active run.
func (d *Daemon) focus(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	src := "ui"
	if r.Header.Get("X-Ship-Client") == "cli" {
		src = "cli"
	}
	paused, err := d.eng.Focus(id, src)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "ship-refresh")
	}
	writeJSON(w, 200, map[string]any{"paused": paused})
}
