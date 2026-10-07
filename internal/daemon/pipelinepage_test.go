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
	h := string(histSVG([]float64{1, 2, 3}, []float64{2, 3, 400}, fmtMS, "run", "v1", "v2"))
	if !strings.Contains(h, "bar-a") || !strings.Contains(h, "bar-b") || !strings.Contains(h, "log scale") {
		t.Fatal(h)
	}
	if histSVG(nil, nil, fmtMS, "run") != "" || sparkSVG([]float64{1}, fmtMS, "visit") != "" {
		t.Fatal("empty input should draw nothing")
	}
	b := string(boxSVG([]float64{1, 2, 3, 4}, []float64{5, 5, 5}, fmtUSD, "v1", "v2"))
	if !strings.Contains(b, "box-a") || !strings.Contains(b, "median") || strings.Contains(b, "NaN") {
		t.Fatal(b)
	}
}

// Every chart carries its details for the tooltip (on hover, focus or
// tap), with a <title> for when there's no JavaScript.
func TestChartTooltips(t *testing.T) {
	ms := []float64{60_000, 61_000, 62_000, 120_000}
	h := string(histSVG(ms, nil, fmtMS, "run"))
	for _, want := range []string{`class="hit"`, `tabindex="0"`, `data-tip="1m 00s to `, `3 runs (75% of runs)`, `<title>1m 00s to `} {
		if !strings.Contains(h, want) {
			t.Fatalf("hist lacks %s:\n%s", want, h)
		}
	}
	two := string(histSVG([]float64{1000, 1000}, []float64{1000, 5000}, fmtMS, "run", "v1", "v2"))
	if !strings.Contains(two, "v1: 2 runs (100% of runs)&#10;v2: 1 run (50% of runs)") {
		t.Fatalf("overlay should give each version's count:\n%s", two)
	}
	b := string(boxSVG([]float64{1, 2, 3, 4, 5}, []float64{2, 4}, num1, "v1", "v2"))
	for _, want := range []string{`data-tip="v1, n=5&#10;min 1 · max 5&#10;quartiles 2 to 4&#10;median 3`, `data-tip="v2, n=2&#10;min 2 · max 4`} {
		if !strings.Contains(b, want) {
			t.Fatalf("box lacks %s:\n%s", want, b)
		}
	}
	sp := string(sparkSVG([]float64{0.5, 0.5, 2}, fmtUSD, "visit"))
	for _, want := range []string{`tabindex="0" data-tip="3 visits, $0.50 to $2.00`, `data-tip="$0.50 to `, `2 visits (67% of visits)`} {
		if !strings.Contains(sp, want) {
			t.Fatalf("spark lacks %s:\n%s", want, sp)
		}
	}
}
