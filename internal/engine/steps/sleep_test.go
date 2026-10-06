package steps

import (
	"context"
	"testing"
	"time"
)

func TestSleepUntilUsesTheWallClock(t *testing.T) {
	// A deadline with a monotonic reading would be compared on the
	// monotonic clock, which stops while a Mac sleeps.
	start := time.Now()
	if err := SleepUntil(context.Background(), start.Add(50*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("returned early")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SleepUntil(ctx, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("ignored cancellation")
	}
}
