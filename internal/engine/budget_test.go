package engine

import (
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
