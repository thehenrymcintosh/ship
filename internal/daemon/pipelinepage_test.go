package daemon

import (
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/pipeline"
)

func TestOutcomeClass(t *testing.T) {
	src := `version: 1
start: impl
steps:
  impl: {prompt: x, next: review}
  review:
    prompt: y
    next: {pass: test, changes: impl, stuck: check}
  test:
    run: make test
    next: {pass: pr, fail: impl}
  pr:
    pr: ""
    next: {feedback: fix, ci_failed: fix, merged: done, closed: stop}
  fix: {prompt: z, next: pr}
  check:
    ask: what now
    choices: {retry: $came_from, give up: stop}
`
	f, fs := pipeline.Parse("p.yml", []byte(src))
	if f == nil {
		t.Fatal(fs)
	}
	p := f.Pipeline
	dist := distToDone(p)
	for _, c := range []struct{ step, outcome, want string }{
		{"impl", "done", "pass"}, // its only way out leads into the loop, but onward
		{"review", "pass", "pass"},
		{"review", "changes", "fail"},
		{"review", "stuck", "fail"}, // a check-in is further from done
		{"test", "fail", "fail"},
		{"pr", "merged", "pass"},
		{"pr", "feedback", "fail"},
		{"pr", "closed", "fail"},
		{"fix", "done", "pass"},
		{"check", "retry", ""},
		{"review", "error", "fail"},
		{"review", "renamed", ""},
	} {
		if got := outcomeClass(p, dist, c.step, c.outcome); got != c.want {
			t.Errorf("%s %s: got %q want %q", c.step, c.outcome, got, c.want)
		}
	}
}

func TestSVGs(t *testing.T) {
	h := string(histSVG([]float64{1, 2, 3}, []float64{2, 3, 400}, fmtMS))
	if !strings.Contains(h, "bar-a") || !strings.Contains(h, "bar-b") || !strings.Contains(h, "log scale") {
		t.Fatal(h)
	}
	if histSVG(nil, nil, fmtMS) != "" || sparkSVG([]float64{1}) != "" {
		t.Fatal("empty input should draw nothing")
	}
	b := string(boxSVG([]float64{1, 2, 3, 4}, []float64{5, 5, 5}, fmtUSD))
	if !strings.Contains(b, "box-a") || !strings.Contains(b, "median") || strings.Contains(b, "NaN") {
		t.Fatal(b)
	}
}
