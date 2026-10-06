package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

const reviewPipe = `version: 1
start: write
steps:
  write:
    prompt: Write the docs.
    next: review
  review:
    prompt: Review the docs.
    next: {pass: done, changes: write}
`

func TestFeedbackVersionsRefine(t *testing.T) {
	h := newHarness(t, map[string]string{"docs": reviewPipe})
	env := []string{"SHIP_HOME=" + h.home}
	ship := func(args ...string) (string, error) {
		cmd := exec.Command(shipBin, append([]string{"--home", h.home}, args...)...)
		cmd.Dir, cmd.Env = h.repo, append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := ship(args...)
		if err != nil {
			t.Fatalf("ship %v: %v\n%s", args, err, out)
		}
		return out
	}

	// A run records the pipeline version it used.
	id := h.startRun("docs", "write: [{outcome: done, summary: wrote}]\nreview: [{outcome: pass, summary: ok}]\n")
	s := h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusDone })
	if s.PipelineVersion != 1 || s.HistoryDir != filepath.Join(h.repo, ".ship", "history", "docs") {
		t.Fatalf("version %d dir %s", s.PipelineVersion, s.HistoryDir)
	}

	// Feedback: on a step (via the daemon), and on the pipeline.
	if out := must("feedback", id, "--step", "write", "too long, no examples"); !strings.Contains(out, "#1 against v1") {
		t.Fatal(out)
	}
	if out, err := ship("feedback", id, "--step", "nope", "x"); err == nil {
		t.Fatalf("unknown step should be rejected: %s", out)
	}
	must("feedback", "--pipeline", "docs", "reviews are rubber stamps")
	out := must("pipeline", "feedback", "docs")
	if !strings.Contains(out, "#1") || !strings.Contains(out, "too long, no examples") || !strings.Contains(out, "#2") {
		t.Fatal(out)
	}

	// Refine with a scripted Claude: rewrite the pipeline, addressing #1.
	newPipe := strings.Replace(reviewPipe, "Write the docs.", "Write concise docs with an example for each command.", 1)
	script, _ := json.Marshal(map[string]any{"refine": []any{map[string]any{"output": map[string]any{
		"summary":    "Ask for concise docs with examples.",
		"changes":    []any{map[string]any{"path": ".ship/pipelines/docs.yml", "content": newPipe, "why": "feedback says the docs are long and lack examples", "addresses": []int{1, 42}}},
		"left_alone": []any{map[string]any{"id": 2, "why": "needs a separate review step; one data point"}},
	}}}})
	fake := filepath.Join(h.home, "refine.yml")
	os.WriteFile(fake, script, 0o644)
	out = must("pipeline", "refine", "docs", "--apply", "--fake-agents", fake)
	if !strings.Contains(out, "is now v2") || !strings.Contains(out, "#2  needs a separate review step") || !strings.Contains(out, "+    prompt: Write concise docs") {
		t.Fatalf("refine output:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(h.repo, ".ship", "pipelines", "docs.yml")); string(b) != newPipe {
		t.Fatal("proposal not applied")
	}
	if m, _ := filepath.Glob(filepath.Join(h.repo, ".ship", "history", "docs", "proposals", "*.md")); len(m) != 1 {
		t.Fatalf("proposal not saved: %v", m)
	}
	out = must("pipeline", "feedback", "docs", "--all")
	if !strings.Contains(out, "addressed in v2") {
		t.Fatalf("#1 should be addressed:\n%s", out)
	}
	// Feedback id 42 doesn't exist, so it isn't recorded as addressed.
	var vs []struct {
		Source    string `json:"source"`
		Addresses []int  `json:"addresses"`
		Frozen    bool   `json:"frozen"`
	}
	vb := must("pipeline", "versions", "docs", "--json")
	if json.Unmarshal([]byte(vb), &vs) != nil || len(vs) != 2 || vs[1].Source != "refine" || fmt.Sprint(vs[1].Addresses) != "[1]" || !vs[1].Frozen {
		t.Fatalf("versions:\n%s", vb)
	}
	// Each version keeps a copy of the pipeline as it was, stored under
	// its content's hash.
	for _, p := range []string{reviewPipe, newPipe} {
		sum := sha256.Sum256([]byte(p))
		if _, err := os.Stat(filepath.Join(h.repo, ".ship", "history", "docs", "objects", hex.EncodeToString(sum[:]))); err != nil {
			t.Fatalf("no copy of %q", p)
		}
	}

	// A hand edit becomes the next version when it's next used; listing
	// the versions only reads.
	os.WriteFile(filepath.Join(h.repo, ".ship", "pipelines", "docs.yml"), []byte(newPipe+"# tweak\n"), 0o644)
	out = must("pipeline", "versions", "docs")
	if strings.Contains(out, "v3") || !strings.Contains(out, "aren't a recorded version yet") {
		t.Fatalf("versions:\n%s", out)
	}

	// Close #2 by hand (recording the edit as v3); then nothing is open,
	// so refine has nothing to do.
	must("pipeline", "close", "docs", "#2", "--note", "added a review step myself")
	out = must("pipeline", "versions", "docs")
	if !strings.Contains(out, "v3") || !strings.Contains(out, "edit") || !strings.Contains(out, "pipeline docs changed") {
		t.Fatalf("versions:\n%s", out)
	}
	if out := must("pipeline", "refine", "docs", "--fake-agents", fake); !strings.Contains(out, "nothing to refine") {
		t.Fatal(out)
	}

	// Report: too little feedback, then enough.
	if out := must("pipeline", "report", "docs", "--fake-agents", fake); !strings.Contains(out, "Not enough feedback") {
		t.Fatal(out)
	}
	must("feedback", id, "the architecture section was spot on")
	rep, _ := json.Marshal(map[string]any{"report": []any{map[string]any{"output": map[string]any{"report": "## Trends\nDocs complaints stopped after v2."}}}})
	os.WriteFile(fake, rep, 0o644)
	out = must("pipeline", "report", "docs", "--fake-agents", fake)
	if !strings.Contains(out, "Docs complaints stopped after v2") {
		t.Fatal(out)
	}
	if m, _ := filepath.Glob(filepath.Join(h.repo, ".ship", "history", "docs", "reports", "*.md")); len(m) != 1 {
		t.Fatal("report not saved")
	}
}

func TestRefineRejectsOutsideFiles(t *testing.T) {
	h := newHarness(t, map[string]string{"docs": reviewPipe})
	cmd := exec.Command(shipBin, "--home", h.home, "feedback", "--pipeline", "docs", "x")
	cmd.Dir = h.repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	script, _ := json.Marshal(map[string]any{"refine": []any{map[string]any{"output": map[string]any{
		"summary": "s", "left_alone": []any{},
		"changes": []any{map[string]any{"path": "src/main.go", "content": "evil", "why": "w", "addresses": []int{1}}},
	}}}})
	fake := filepath.Join(h.home, "refine.yml")
	os.WriteFile(fake, script, 0o644)
	cmd = exec.Command(shipBin, "--home", h.home, "pipeline", "refine", "docs", "--apply", "--fake-agents", fake)
	cmd.Dir = h.repo
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "changes a file it shouldn't") {
		t.Fatalf("want rejection, got %v:\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(h.repo, "src", "main.go")); err == nil {
		t.Fatal("wrote outside the allowed files")
	}
}

func TestPipelineStatsAndStatusTokens(t *testing.T) {
	h := newHarness(t, map[string]string{"docs": reviewPipe})
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(shipBin, append([]string{"--home", h.home}, args...)...)
		cmd.Dir, cmd.Env = h.repo, append(os.Environ(), "SHIP_HOME="+h.home, "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ship %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	script := `write: [{outcome: done, summary: wrote, cost: 0.5, usage: {input: 1000, output: 2000, cache_write: 3000, cache_read: 40000}}]
review:
  - {outcome: changes, summary: again, usage: {input: 10, output: 20}}
  - {outcome: pass, summary: ok, usage: {input: 10, output: 20}}
`
	id1 := h.startRun("docs", script)
	h.waitFor(id1, func(s *store.RunSnapshot) bool { return s.Status == store.StatusDone })
	id2 := h.startRun("docs", "write: [{outcome: done, summary: wrote, tokens: 4000}]\nreview: [{outcome: pass, summary: ok}]\n")
	h.waitFor(id2, func(s *store.RunSnapshot) bool { return s.Status == store.StatusDone })

	out := run("status", id1)
	if !strings.Contains(out, "6k tok") {
		t.Fatalf("status has no tokens column:\n%s", out)
	}
	var s store.RunSnapshot
	if err := json.Unmarshal([]byte(run("status", id1, "--json")), &s); err != nil {
		t.Fatal(err)
	}
	if u := s.Visits[0].Usage; u == nil || u.CacheRead != 40000 || u.Output != 2000 {
		t.Fatalf("status --json usage %+v", u)
	}

	// The run page: tokens on each visit, and the per-step table.
	var page []byte
	if err := h.c.Do("GET", "/fragments/runs/"+id1+"/timeline", nil, &page); err != nil || !strings.Contains(string(page), "6k tok · in 1k / out 2k / cache 3k write, 40k read") {
		t.Fatalf("timeline: %v\n%s", err, page)
	}
	if err := h.c.Do("GET", "/fragments/runs/"+id1+"/steps", nil, &page); err != nil || !strings.Contains(string(page), "worth splitting") || !strings.Contains(string(page), "80k") {
		t.Fatalf("steps: %v\n%s", err, page)
	}

	var st pipelineStats
	if err := json.Unmarshal([]byte(run("pipeline", "stats", "docs", "--json")), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Runs) != 2 || len(st.Steps) != 2 {
		t.Fatalf("stats %+v", st)
	}
	// The first run wrote twice (6000 tokens each), the second once (4000);
	// review ran twice, then once.
	w, r := st.Steps[0], st.Steps[1]
	if w.Step != "write" || w.Tokens != 8000 || w.Usage.CacheRead != 40000 || w.CostUSD != 0.5 || r.Visits != 1.5 || r.Runs != 2 {
		t.Fatalf("averages %+v %+v", w, r)
	}
	out = run("pipeline", "stats", "docs")
	if !strings.Contains(out, "last 2 runs") || !strings.Contains(out, "Hands-on: 0 per run") || !strings.Contains(out, "100% were fully autonomous") || !strings.Contains(out, "CACHE READ") || !strings.Contains(out, "40k") || !strings.Contains(out, "1.5") {
		t.Fatalf("stats:\n%s", out)
	}
}
