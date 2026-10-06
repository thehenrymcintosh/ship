package steps

import (
	"context"
	"testing"
	"time"
)

func TestSleepUntilUsesTheWallClock(t *testing.T) {
	// A deadline with a monotonic reading would be compared on the
	// monotonic clock, which stops while a Mac sleeps.
	// The deadline is on the wall clock, so that's what it's measured
	// against: the monotonic clock can run a little behind it.
	start := time.Now().Round(0)
	if err := SleepUntil(context.Background(), start.Add(50*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if time.Now().Round(0).Sub(start) < 50*time.Millisecond {
		t.Fatal("returned early")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SleepUntil(ctx, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("ignored cancellation")
	}
}
