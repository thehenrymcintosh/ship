package engine

import (
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// RecordRunStats writes a finished run's stats into its pipeline's
// history (runs/<run-id>.json), if the config wants them. It reports
// whether it wrote.
func (e *Engine) RecordRunStats(s *store.RunSnapshot, cfg config.Config) bool {
	if s == nil || !s.Status.Terminal() || !cfg.Stats.RecordRuns || (s.FakeAgents != "" && !cfg.Stats.IncludeFakeRuns) {
		return false
	}
	dir := RunHistoryDir(s, e.Home())
	hs := e.HistoryStore(dir)
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

// BackfillStats records stats for finished runs of a pipeline (in repo;
// "" for any) that don't have them yet. It returns how many it wrote.
func (e *Engine) BackfillStats(repo, pipe string) (int, error) {
	snaps, err := e.o.Store.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range snaps {
		if s.Pipeline != pipe || !s.Status.Terminal() || (repo != "" && s.Repo != repo) {
			continue
		}
		cfg, err := e.o.LoadConfig(s.Repo)
		if err != nil {
			cfg = config.Defaults()
		}
		if e.RecordRunStats(s, cfg) {
			n++
		}
	}
	return n, nil
}
