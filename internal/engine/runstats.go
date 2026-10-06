package engine

import (
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// wantsStats reports whether a run's stats should be recorded: it's
// finished, and the config records runs like it.
func wantsStats(s *store.RunSnapshot, cfg config.Config) bool {
	return s != nil && s.Status.Terminal() && cfg.Stats.RecordRuns && (s.FakeAgents == "" || cfg.Stats.IncludeFakeRuns)
}

// RecordRunStats writes a finished run's stats into its pipeline's
// history (runs/<run-id>.json), if the config wants them. It reports
// whether it wrote.
func (e *Engine) RecordRunStats(s *store.RunSnapshot, cfg config.Config) bool {
	if !wantsStats(s, cfg) {
		return false
	}
	hs := e.HistoryStore(RunHistoryDir(s, e.Home()))
	if hs.HasRun(s.ID) {
		return false
	}
	events, _, err := store.ReadEvents(e.o.Store.RunDir(s.ID))
	if err != nil {
		e.o.Log.Warn("run stats", "run", s.ID, "err", err)
		return false
	}
	if err := hs.WriteRun(history.BuildRunStats(s, events)); err != nil {
		e.o.Log.Warn("run stats", "run", s.ID, "err", err)
		return false
	}
	return true
}

type unrecordedRun struct {
	snap *store.RunSnapshot
	cfg  config.Config
}

// unrecorded lists this machine's finished runs of pipeline pipe whose
// stats belong in the history at dir but aren't there.
func (e *Engine) unrecorded(dir, pipe string) ([]unrecordedRun, error) {
	snaps, err := e.o.Store.List()
	if err != nil {
		return nil, err
	}
	cfgs := map[string]config.Config{}
	var out []unrecordedRun
	for _, s := range snaps {
		if s.Pipeline != pipe || !s.Status.Terminal() {
			continue
		}
		cfg, ok := cfgs[s.Repo]
		if !ok {
			if cfg, err = e.o.LoadConfig(s.Repo); err != nil {
				cfg = config.Defaults()
			}
			cfgs[s.Repo] = cfg
		}
		if !wantsStats(s, cfg) || RunHistoryDir(s, e.Home()) != dir || e.HistoryStore(dir).HasRun(s.ID) {
			continue
		}
		out = append(out, unrecordedRun{s, cfg})
	}
	return out, nil
}

// UnrecordedRuns counts this machine's finished runs of a pipeline that
// have no stats in its history (at dir) yet, such as runs from before ship
// kept them. It writes nothing.
func (e *Engine) UnrecordedRuns(dir, pipe string) int {
	runs, _ := e.unrecorded(dir, pipe)
	return len(runs)
}

// BackfillStats records the stats of the runs UnrecordedRuns counts. It
// returns how many it wrote.
func (e *Engine) BackfillStats(dir, pipe string) (int, error) {
	runs, err := e.unrecorded(dir, pipe)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		if e.RecordRunStats(r.snap, r.cfg) {
			n++
		}
	}
	return n, nil
}
