package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

// The pipeline page, from a test daemon with fake-agent runs on two
// versions.
func TestPipelinePage(t *testing.T) {
	h := newHarness(t, map[string]string{"docs": reviewPipe})
	run := func(script string) {
		t.Helper()
		id := h.startRun("docs", script)
		h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusDone })
	}
	slow := "write: [{outcome: done, summary: w, cost: 0.9}]\nreview: [{outcome: changes, summary: again, cost: 0.5}, {outcome: changes, summary: again, cost: 0.5}, {outcome: pass, summary: ok, cost: 0.5}]\n"
	fast := "write: [{outcome: done, summary: w, cost: 0.3}]\nreview: [{outcome: pass, summary: ok, cost: 0.2}]\n"
	for i := 0; i < 4; i++ {
		run(slow)
	}
	os.WriteFile(filepath.Join(h.repo, ".ship", "pipelines", "docs.yml"), []byte(strings.Replace(reviewPipe, "Write the docs.", "Write short docs.", 1)), 0o644)
	for i := 0; i < 4; i++ {
		run(fast)
	}
	if m, _ := filepath.Glob(filepath.Join(h.repo, ".ship", "history", "docs", "runs", "*.json")); len(m) != 8 {
		t.Fatalf("run records: %v", m)
	}

	var page []byte
	if err := h.c.Do("GET", "/pipelines/docs?repo="+url.QueryEscape(h.repo), nil, &page); err != nil {
		t.Fatal(err)
	}
	p := string(page)
	for _, want := range []string{
		"Overview", "8 recorded runs", "Hands-on", "On its own",
		`<svg class="hist"`, `<svg class="spark"`, `<svg class="box"`,
		"Versions", "pipeline docs changed", "pipeline restore docs v1",
		"Compare versions", `<option value="v1" selected>`, `<option value="v2" selected>`,
		"Run cost", "likely better", "Pass rate", "Needs a person",
		"v2 looks like an improvement on v1",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(p, "—") {
		t.Error("em dash in the page")
	}
	// review sent work back 8 times out of 12 on v1: its pass rate is shown.
	if !strings.Contains(p, "(8/16)") {
		t.Errorf("review pass count missing")
	}
	// Choosing the same version on both sides still renders.
	if err := h.c.Do("GET", "/pipelines/docs?a=v2&b=v2&repo="+url.QueryEscape(h.repo), nil, &page); err != nil || !strings.Contains(string(page), "No clear difference") {
		t.Fatalf("%v", err)
	}
	// The pipelines page links to it.
	if err := h.c.Do("GET", "/pipelines?repo="+url.QueryEscape(h.repo), nil, &page); err != nil || !strings.Contains(string(page), `href="/pipelines/docs?repo=`) {
		t.Fatalf("pipelines page: %v", err)
	}
	if err := h.c.Do("GET", "/pipelines/nope", nil, &page); err == nil {
		t.Fatal("unknown pipeline should 404")
	}
}
