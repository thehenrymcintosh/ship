package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/engine/steps"
	"github.com/thehenrymcintosh/ship/internal/store"
)

func visitDir(en *env, s *store.RunSnapshot, i int) string {
	return filepath.Join(en.st.RunDir(s.ID), store.VisitsDir, s.Visits[i].Dir)
}

func TestAgentDecisionDrivesCheckIn(t *testing.T) {
	en := newEnv(t, map[string]string{"p": checkInPipe})
	s := en.start("p", "", nil, `impl: [{outcome: done, summary: ok}]
review:
  - outcome: stuck
    summary: two findings
    decision: {headline: "Fix R1 or ship as is?", situation: "R1 is a nil check.", recommended: again, reason: "It's a one-line fix.",
      options: [{choice: again, consequence: "impl fixes R1"}, {choice: abandon, consequence: "the run stops"}]}
check.summary: [{output: {headline: "should not run", situation: "", recommended: again, reason: ""}}]
`)
	s = en.waitStatus(s.ID, store.StatusAsking)
	d := steps.ReadDecision(visitDir(en, s, 1))
	if d == nil || d.Headline != "Fix R1 or ship as is?" || d.Recommended != "again" || len(d.Options) != 2 {
		t.Fatalf("decision: %+v", d)
	}
	// With the agent's own decision there's nothing to summarise.
	if steps.ReadCheckInSummary(visitDir(en, s, 2)) != nil {
		t.Fatal("summarised a check-in that had a decision")
	}
}

func TestInvalidDecisionIsRejected(t *testing.T) {
	en := newEnv(t, map[string]string{"p": checkInPipe})
	s := en.start("p", "", nil, `impl: [{outcome: done, summary: ok}]
review: [{outcome: stuck, summary: hmm, decision: {headline: "Which?", situation: "", recommended: maybe, reason: ""}}]
`)
	s = en.waitStatus(s.ID, store.StatusNeedsAttention)
	lv := s.LastVisit()
	if lv.Error == nil || lv.Error.Reason != "invalid_output" || !strings.Contains(lv.Error.Message, "/decision/recommended") {
		t.Fatalf("%+v", lv.Error)
	}
}

func TestCheckInSummaryFallback(t *testing.T) {
	en := newEnv(t, map[string]string{"p": checkInPipe})
	s := en.start("p", "", nil, `impl: [{outcome: done, summary: ok, cost: 0.5}]
review: [{outcome: stuck, summary: "The tests can't run without a database."}]
check.summary: [{cost: 0.01, output: {headline: "Set up a database, or stop?", situation: "Tests need Postgres.", recommended: abandon, reason: "Nothing to fix in code.",
  options: [{choice: again, consequence: "impl tries again"}]}}]
`)
	s = en.waitFor(s.ID, "summary", func(s *store.RunSnapshot) bool { return s.Status == store.StatusAsking && s.CostUSD > 0.5 })
	sum := steps.ReadCheckInSummary(visitDir(en, s, 2))
	if sum == nil || sum.Headline != "Set up a database, or stop?" || sum.Recommended != "abandon" || sum.Model != steps.SummaryModel {
		t.Fatalf("summary: %+v", sum)
	}
	if s.CostUSD != 0.51 {
		t.Fatalf("the summary's cost should count toward the run's: %v", s.CostUSD)
	}
}

func TestCheckInSummaryCanBeTurnedOff(t *testing.T) {
	en := newEnv(t, map[string]string{"p": checkInPipe})
	en.cfg.CheckIns.Summarize = false
	s := en.start("p", "", nil, `impl: [{outcome: done, summary: ok}]
review: [{outcome: stuck, summary: hmm}]
check.summary: [{output: {headline: "no", situation: "", recommended: again, reason: ""}}]
`)
	s = en.waitStatus(s.ID, store.StatusAsking)
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "abandon"}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusStopped)
	if steps.ReadCheckInSummary(visitDir(en, s, 2)) != nil {
		t.Fatal("summarised with checkins.summarize off")
	}
}
