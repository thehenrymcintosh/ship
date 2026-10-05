package engine

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/thehenrymcintosh/ship/internal/engine/steps"
	"github.com/thehenrymcintosh/ship/internal/store"
)

func fastLimits(t *testing.T) {
	m, b := steps.LimitMargin, steps.LimitBackoff
	steps.LimitMargin, steps.LimitBackoff = 0, 20*time.Millisecond
	t.Cleanup(func() { steps.LimitMargin, steps.LimitBackoff = m, b })
}

func TestUsageLimitIsWaitedOut(t *testing.T) {
	fastLimits(t)
	en := newEnv(t, map[string]string{"p": `version: 1
start: work
steps:
  work: {prompt: Do it., next: done}
`})
	// Hits a limit that resets in a second, then one with no reset time.
	s := en.start("p", "", nil, `work: [{outcome: done, summary: ok, limited: 2, limit_reset: 1s}]`)
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "work:done" {
		t.Fatalf("trail %q", got)
	}
	evs, _, _ := store.ReadEvents(en.e.o.Store.RunDir(s.ID))
	waited := false
	for _, ev := range evs {
		if ev.Type == store.EvStatusChanged && strings.Contains(string(ev.Data), "usage limit") {
			waited = true
		}
	}
	if !waited {
		t.Fatal("the run never showed it was waiting for the limit")
	}
}

// A limit reported by an invocation that still finished isn't waited out:
// the result is kept.
func TestUsageLimitNoticeOnSuccessIsIgnored(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: work
steps:
  work: {prompt: Do it., next: done}
`})
	s := en.start("p", "", nil, `work: [{outcome: done, summary: finished anyway, limit_notice: true}]`)
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "work:done" || s.Visits[0].Summary != "finished anyway" {
		t.Fatalf("trail %q summary %q", got, s.Visits[0].Summary)
	}
}

// A budget stop that also carried a limit report is a budget stop: it routes
// at once instead of waiting for the limit to reset.
func TestBudgetStopWithLimitNoticeRoutesAtOnce(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: work
steps:
  work: {prompt: Do it., max_tokens: 50, on_error: fallback, next: done}
  fallback: {run: "true", next: done}
`})
	s := en.start("p", "", nil, `work: [{outcome: done, summary: big, tokens: 200, limit_notice: true}]`)
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "work:error fallback:pass" || s.Visits[0].Error == nil || s.Visits[0].Error.Reason != "budget" {
		t.Fatalf("trail %q error %+v", got, s.Visits[0].Error)
	}
}

func TestRunTokenBudgetHoldsTheRun(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: work
limits: {max_tokens: 100}
defaults: {on_error: stop}
steps:
  work: {prompt: Do it., next: done}
`})
	s := en.start("p", "", nil, `work:
  - {outcome: done, summary: big, tokens: 150}
  - {outcome: done, summary: small, tokens: 50}
`)
	// Stopping at the run's budget holds the run (on_error isn't used).
	s = en.waitStatus(s.ID, store.StatusNeedsAttention)
	if !strings.Contains(s.StatusReason, "budget reached") || s.Tokens != 101 {
		t.Fatalf("reason %q tokens %d", s.StatusReason, s.Tokens)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdRaiseBudget, USD: 5}); KindOf(err) != KindInvalid {
		t.Fatalf("raising a dollar budget the run doesn't have: %v", err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdRaiseBudget, Tokens: 100, Action: "retry"}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	if s.ExtraTokens != 100 || s.Tokens != 151 || visitTrail(s) != "work:error work:done" {
		t.Fatalf("extra %d tokens %d trail %q", s.ExtraTokens, s.Tokens, visitTrail(s))
	}
}

func TestStepBudgetRoutesLikeAnError(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: work
steps:
  work: {prompt: Do it., max_budget_usd: 1, on_error: fallback, next: done}
  fallback: {run: "true", next: done}
`})
	s := en.start("p", "", nil, `work: [{outcome: done, summary: pricey, cost: 2}]`)
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "work:error fallback:pass" {
		t.Fatalf("trail %q", got)
	}
	if lv := s.Visits[0]; lv.Error == nil || lv.Error.Reason != "budget" {
		t.Fatalf("error %+v", lv.Error)
	}
}

// claude stopped at a token budget never reports its cost, so it's
// estimated from what the run's agents have cost per token so far.
func TestTokenBudgetStopEstimatesCost(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: plan
steps:
  plan: {prompt: Plan it., next: work}
  work: {prompt: Do it., max_tokens: 50, on_error: fallback, next: done}
  fallback: {run: "true", next: done}
`})
	s := en.start("p", "", nil, `plan: [{outcome: done, summary: ok, cost: 1, tokens: 100}]
work: [{outcome: done, summary: big, tokens: 200}]
`)
	s = en.waitStatus(s.ID, store.StatusDone)
	w := s.Visits[1]
	// The fake stops at 51 tokens; the run has cost $0.01 a token.
	if w.Error == nil || w.Error.Reason != "budget" || !strings.Contains(w.Error.Message, "cost estimated at $0.51") || math.Abs(w.CostUSD-0.51) > 1e-9 {
		t.Fatalf("cost %v error %+v", w.CostUSD, w.Error)
	}
}

func TestTokenBreakdownPerVisit(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: work
steps:
  work: {prompt: Do it., next: check}
  check: {prompt: Check it., next: {again: work, ok: done}}
`})
	// work's first visit needs a correction, so its two tries are summed.
	s := en.start("p", "", nil, `work:
  - {outcome: done, summary: ok, invalid_attempts: 1, usage: {input: 10, output: 20, cache_write: 30, cache_read: 400}}
check:
  - {outcome: again, summary: more, usage: {input: 1, output: 2, cache_write: 3, cache_read: 4}}
  - {outcome: ok, summary: fine, usage: {input: 1, output: 2, cache_write: 3, cache_read: 4}}
`)
	s = en.waitStatus(s.ID, store.StatusDone)
	w := s.Visits[0]
	if w.Usage == nil || *w.Usage != (store.TokenUsage{Input: 20, Output: 40, CacheWrite: 60, CacheRead: 800}) || w.Tokens != 120 {
		t.Fatalf("work usage %+v tokens %d", w.Usage, w.Tokens)
	}
	stats := store.StepStats(s.Visits)
	if len(stats) != 2 || stats[0].Step != "work" || stats[0].Visits != 2 || stats[1].Visits != 2 {
		t.Fatalf("stats %+v", stats)
	}
	if c := stats[1]; c.Tokens != 12 || c.Usage != (store.TokenUsage{Input: 2, Output: 4, CacheWrite: 6, CacheRead: 8}) {
		t.Fatalf("check stats %+v", c)
	}
	// The breakdown survives a rebuild from the log.
	re, _, err := store.Rebuild(en.e.o.Store.RunDir(s.ID))
	if err != nil {
		t.Fatal(err)
	}
	if u := re.Visits[0].Usage; u == nil || u.CacheRead != 800 {
		t.Fatalf("rebuilt usage %+v", u)
	}
	// work ran twice in one of two runs: once per run on average.
	avg := store.AverageStepStats([]*store.RunSnapshot{s, {}})
	if avg[0].Runs != 1 || avg[0].Visits != 1 || avg[0].Tokens != 120 || avg[0].Usage.CacheRead != 800 {
		t.Fatalf("averages %+v", avg[0])
	}
}
