package history

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
)

func TestBuildRunStatsHumanAndPR(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	fin := func(m int) *time.Time { x := at(m); return &x }
	s := &store.RunSnapshot{
		ID: "r", Pipeline: "p", Status: store.StatusDone, CreatedAt: at(0), FinishedAt: fin(100),
		Visits: []store.VisitSummary{
			{Seq: 1, Step: "impl", Type: "agent", Outcome: "done", Finished: fin(5), DurationMS: 300000},
			{Seq: 2, Step: "test", Type: "run", Outcome: "error", Error: &store.StepError{Reason: "timeout"}, Finished: fin(10), DurationMS: 1000},
			{Seq: 3, Step: "test", Type: "run", Outcome: "pass", Finished: fin(20), DurationMS: 1000},
			{Seq: 4, Step: "watch", Type: "pr", Outcome: "feedback", Summary: "3 new piece(s) of review feedback on PR #1", Finished: fin(30)},
			{Seq: 5, Step: "fix", Type: "agent", Outcome: "done", Finished: fin(35)},
			{Seq: 6, Step: "watch", Type: "pr", Outcome: "ci_failed", Finished: fin(40)},
			{Seq: 7, Step: "watch", Type: "pr", Outcome: "merged", Finished: fin(90)},
		},
	}
	ev := func(m int, typ, actor string, data any) store.Event {
		b, _ := json.Marshal(data)
		return store.Event{TS: at(m), Type: typ, Actor: actor, Data: b}
	}
	events := []store.Event{
		ev(0, store.EvRunCreated, "engine", store.RunCreated{Start: "impl"}),
		ev(0, store.EvVisitStarted, "engine", store.VisitStarted{Seq: 1, Step: "impl"}),
		ev(1, store.EvCommand, "user", store.Command{Name: "pause", Source: "ui"}),
		ev(2, store.EvCommand, "user", store.Command{Name: "resume", Source: "ui"}),
		ev(5, store.EvTransition, "engine", store.Transition{From: "impl", To: "test"}),
		ev(5, store.EvVisitStarted, "engine", store.VisitStarted{Seq: 2, Step: "test"}),
		ev(10, store.EvStatusChanged, "engine", store.StatusChanged{To: store.StatusNeedsAttention}),
		ev(14, store.EvCommand, "user", store.Command{Name: "retry", Source: "cli"}),
		ev(14, store.EvStatusChanged, "engine", store.StatusChanged{To: store.StatusRunning}),
		ev(14, store.EvVisitStarted, "engine", store.VisitStarted{Seq: 3, Step: "test"}),
		ev(20, store.EvVisitStarted, "engine", store.VisitStarted{Seq: 4, Step: "watch"}),
		ev(50, store.EvCommand, "user", store.Command{Name: "cancel", Source: "engine"}), // engine-sourced: ignored
	}
	rs := BuildRunStats(s, events)
	h := rs.Human
	if h.Interventions["retry"] != 1 || h.Interventions["pause"] != 1 || h.Corrective() != 1 || h.HandsOn() != 1 || h.Autonomous {
		t.Fatalf("human: %+v", h)
	}
	if h.WaitMS != 4*60*1000 {
		t.Fatalf("wait %d", h.WaitMS)
	}
	if rs.PR == nil || rs.PR.FeedbackBatches != 1 || rs.PR.Comments != 3 || rs.PR.CIFailures != 1 || rs.PR.Rounds != 2 || !rs.PR.Merged {
		t.Fatalf("pr: %+v", rs.PR)
	}
	var test StepStats
	for _, st := range rs.Steps {
		if st.Step == "test" {
			test = st
		}
	}
	if test.Visits != 2 || test.Errors["timeout"] != 1 || test.Human.Interventions["retry"] != 1 || test.Human.WaitMS != 4*60*1000 {
		t.Fatalf("test step: %+v", test)
	}
	if rs.DurationMS != 100*60*1000 {
		t.Fatal(rs.DurationMS)
	}

	// Only scheduling before the PR: still autonomous.
	rs = BuildRunStats(s, events[:5])
	if !rs.Human.Autonomous {
		t.Fatalf("%+v", rs.Human)
	}
}

// An answer given by an `answer` command doesn't undo the unrelated
// intervention issued just before it.
func TestBuildRunStatsAnswerCommand(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fin := t0.Add(time.Hour)
	s := &store.RunSnapshot{ID: "r", Pipeline: "p", Status: store.StatusDone, CreatedAt: t0, FinishedAt: &fin,
		Visits: []store.VisitSummary{{Seq: 1, Step: "plan", Type: "ask", Outcome: "yes", Finished: &fin}}}
	ev := func(typ, actor string, data any) store.Event {
		b, _ := json.Marshal(data)
		return store.Event{TS: t0, Type: typ, Actor: actor, Data: b}
	}
	rs := BuildRunStats(s, []store.Event{
		ev(store.EvRunCreated, "engine", store.RunCreated{Start: "plan"}),
		ev(store.EvVisitStarted, "engine", store.VisitStarted{Seq: 1, Step: "plan"}),
		ev(store.EvCommand, "user", store.Command{Name: "pause", Source: "cli"}),
		ev(store.EvCommand, "user", store.Command{Name: "answer", Source: "ui"}),
		ev(store.EvAskAnswered, "user", store.AskAnswered{Seq: 1, Choice: "yes"}),
	})
	if h := rs.Human; h.Interventions["pause"] != 1 || h.CheckIns != 1 {
		t.Fatalf("human: %+v", h)
	}
}
