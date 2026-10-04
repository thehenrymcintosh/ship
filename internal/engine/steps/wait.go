package steps

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
	"github.com/thehenrymcintosh/ship/internal/tmpl"
)

// Wait polling limits.
const (
	MaxPollFailures = 5
	KeepPolls       = 50
	pollTimeoutCap  = 10 * time.Minute
)

// Wait executes `wait` steps: one visit that polls a command.
type Wait struct {
	// Now and Sleep are overridable in tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

func (w Wait) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w Wait) sleep(ctx context.Context, d time.Duration) error {
	if w.Sleep != nil {
		return w.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Execute polls until the last line names an outcome, the wait times out,
// or polling fails MaxPollFailures times in a row.
func (w Wait) Execute(ctx context.Context, v *Visit) (Result, error) {
	if err := v.RT.SetStatus(store.StatusWaiting, "wait: "+v.StepName); err != nil {
		return Result{}, err
	}
	started := v.StartedAt
	if started.IsZero() {
		started = w.now()
	}
	deadline := started.Add(v.Timeout)
	every := v.Step.EveryD()
	n := 0
	if vs := v.Snapshot.Visit(v.Seq); vs != nil {
		n = vs.Polls
	}
	var sections []string
	if b, err := os.ReadFile(v.File("stdout.log")); err == nil && len(b) > 0 {
		sections = splitPolls(string(b))
	}
	failures := 0
	var lastOut []string
	for {
		if v.Timeout > 0 && !w.now().Before(deadline) {
			if t, ok := v.Step.Target("timeout"); ok && t != "" {
				return Result{Outcome: "timeout", Summary: fmt.Sprintf("Waited %s without a result", v.Timeout), Polls: n, Output: lastOut}, nil
			}
			r := ErrorResult("timeout", fmt.Sprintf("wait timed out after %s", v.Timeout))
			r.Polls, r.Output = n, lastOut
			return r, nil
		}
		n++
		script, err := tmpl.Render(pipeline.Str(v.Step.Wait), v.Scope, tmpl.Shell)
		if err != nil {
			return renderError(err), nil
		}
		if n == 1 || !v.Resumed {
			_ = writeFile(v.File("input.md"), "```bash\n"+script+"\n```\n\nPolled every "+every.String()+".\n")
		}
		pv := *v
		pv.Timeout = pollTimeoutCap
		if rem := deadline.Sub(w.now()); v.Timeout > 0 && rem < pv.Timeout && rem > 0 {
			pv.Timeout = rem
		}
		res, out, rerr := runScript(ctx, &pv, script, false)
		if ctx.Err() != nil {
			return Result{Outcome: OutcomeCancelled, Summary: "cancelled", Polls: n}, nil
		}
		lines := out.tail.Lines()
		lastOut = lines
		last := lastLine(out.stdout.String())
		exit := res.ExitCode
		if rerr != nil {
			exit = -1
		}
		sections = append(sections, fmt.Sprintf("--- poll %d · %s · exit %d ---\n%s", n, w.now().Format(time.RFC3339), exit, strings.Join(lines, "\n")))
		if len(sections) > KeepPolls {
			sections = sections[len(sections)-KeepPolls:]
		}
		_ = writeFile(v.File("stdout.log"), strings.Join(sections, "\n")+"\n")
		if err := v.RT.Emit(store.EvWaitPolled, store.WaitPolled{Seq: v.Seq, N: n, Exit: exit, LastLine: truncate(last, 200)}); err != nil {
			return Result{}, err
		}
		if rerr != nil || exit != 0 {
			failures++
			if failures >= MaxPollFailures {
				r := ErrorResult("poll_failures", fmt.Sprintf("the wait command failed %d times in a row (last exit %d)", failures, exit))
				r.Polls, r.Output = n, lines
				return r, nil
			}
		} else {
			failures = 0
			if last != "" && last != pipeline.OutcomeError {
				if _, ok := v.Step.Target(last); ok && v.Step.Next != nil && v.Step.Next.IsMap {
					return Result{Outcome: last, Summary: fmt.Sprintf("Poll %d printed %q", n, last), Polls: n, Output: lines}, nil
				}
			}
		}
		wait := every
		if v.Timeout > 0 {
			if rem := deadline.Sub(w.now()); rem < wait {
				wait = rem
			}
		}
		if wait > 0 {
			if err := w.sleep(ctx, wait); err != nil {
				return Result{Outcome: OutcomeCancelled, Summary: "cancelled", Polls: n}, nil
			}
		}
	}
}

func splitPolls(s string) []string {
	var out []string
	for _, part := range strings.Split(s, "\n--- poll ") {
		part = strings.TrimSuffix(part, "\n")
		if part == "" {
			continue
		}
		if !strings.HasPrefix(part, "--- poll ") {
			part = "--- poll " + part
		}
		out = append(out, part)
	}
	return out
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
