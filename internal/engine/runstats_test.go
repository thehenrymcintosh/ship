package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/store"
)

const checkInPipe = `version: 1
start: impl
steps:
  impl: {prompt: Implement it., next: review}
  review:
    prompt: Review it.
    next: {pass: done, stuck: check}
  check:
    ask: Stuck. What now?
    choices: {again: impl, abandon: stop}
`

func TestRunStatsRecorded(t *testing.T) {
	en := newEnv(t, map[string]string{"p": checkInPipe})
	en.cfg.Stats.IncludeFakeRuns = true
	s := en.start("p", "", nil, "impl: [{outcome: done, summary: ok, cost: 0.5, tokens: 100}]\nreview: [{outcome: stuck, summary: hmm}, {outcome: pass, summary: ok}]\n")
	en.waitStatus(s.ID, store.StatusAsking)
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "again", Note: "try harder", For: store.NoteForRun}); err != nil {
		t.Fatal(err)
	}
	snap := en.waitStatus(s.ID, store.StatusDone)
	hs := history.Open(filepath.Join(en.repo, ".ship", "history", "p"), t.TempDir())
	runs, err := hs.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("%v %v", runs, err)
	}
	r := runs[0]
	if r.Run != s.ID || r.Status != store.StatusDone || r.Version != snap.PipelineHash || r.CostUSD != 1 {
		t.Fatalf("%+v", r)
	}
	h := r.Human
	if h.CheckIns != 1 || h.NotesRun != 1 || h.Autonomous || h.Corrective() != 0 || h.WaitMS <= 0 {
		t.Fatalf("human: %+v", h)
	}
	steps := map[string]history.StepStats{}
	for _, st := range r.Steps {
		steps[st.Step] = st
	}
	if rv := steps["review"]; rv.Visits != 2 || rv.Outcomes["stuck"] != 1 || rv.Outcomes["pass"] != 1 || len(rv.DurationsMS) != 2 {
		t.Fatalf("review: %+v", rv)
	}
	if c := steps["check"]; c.Choices["again"] != 1 || c.Human.CheckIns != 1 {
		t.Fatalf("check: %+v", c)
	}
	// Written once; a backfill doesn't add another.
	if n, _ := en.e.BackfillStats(en.repo, "p"); n != 0 {
		t.Fatal("backfill rewrote a recorded run")
	}
	// Without the file, backfill recreates it from the event log.
	os.Remove(filepath.Join(hs.Dir, "runs", s.ID+".json"))
	if n, _ := en.e.BackfillStats(en.repo, "p"); n != 1 {
		t.Fatal("backfill didn't record the run")
	}
	// Dry runs aren't recorded by default.
	en.cfg.Stats.IncludeFakeRuns = false
	s2 := en.start("p", "", nil, "impl: [{outcome: done, summary: ok}]\nreview: [{outcome: pass, summary: ok}]\n")
	en.waitStatus(s2.ID, store.StatusDone)
	if hs.HasRun(s2.ID) {
		t.Fatal("fake run recorded")
	}
}
