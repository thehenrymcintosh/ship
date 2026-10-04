package engine

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

const e2eFeature = `version: 1
start: split
variables:
  ticket: {from_brief: true, format: '^[A-Z]+-[0-9]+$'}
defaults: {on_error: chef}
steps:
  split:
    split: /split
    review: true
    next: {ok: build, unclear: chef}
  build:
    fanout: slice
    mode: %s
    stack: %v
    advance_on: %s
    max_parallel: 2
    next: {done: done, failed: chef}
  chef:
    ask: "stopped at {{came_from}}"
    choices: {retry: $came_from, abandon: stop}
`

const e2eSlice = `version: 1
start: implement
workspace:
  branch: "{{parent.vars.ticket}}/{{slice.number}}-{{slice.key}}"
variables:
  test: { value: "true" }
defaults: {max_visits: 3, when_exhausted: chef, on_error: chef}
steps:
  implement:
    agent: /implement
    session: continue
    next: review
  review:
    agent: /review
    next: {pass: gate, changes: implement, stuck: chef}
  gate:
    description: Clean tree and green tests
    run: |
      test -z "$(git status --porcelain)" || { echo "uncommitted changes"; exit 1; }
      {{raw vars.test}}
    next: {pass: at-pass, fail: implement}
  at-pass:
    wait: .ship/bin/pr-status
    every: 50ms
    next: {merged: done, comments: implement}
  chef:
    ask: "{{slice.title}} stopped at {{came_from}}"
    choices: {retry: $came_from, abandon: stop}
`

func e2eScript(n int) string {
	var slices strings.Builder
	for i := 0; i < n; i++ {
		k := string(rune('a' + i))
		fmt.Fprintf(&slices, "      - {key: %s, title: Slice %s, brief: \"do %s\", acceptance: [\"%s works\"]}\n", k, strings.ToUpper(k), k, k)
	}
	return `split:
  - outcome: ok
    summary: split it
    slices:
` + slices.String() + `implement:
  - {outcome: done, summary: built, run: 'echo "$SHIP_RUN_ID $SHIP_VISIT_SEQ" >> "work-${SHIP_RUN_ID##*.}.txt" && git add -A && git commit -qm "$SHIP_RUN_ID"'}
review:
  - {outcome: changes, summary: "missing tests"}
  - {outcome: pass, summary: lgtm}
`
}

func newE2E(t *testing.T, mode string, stack bool, advance string) *env {
	en := newEnv(t, map[string]string{
		"feature": fmt.Sprintf(e2eFeature, mode, stack, advance),
		"slice":   e2eSlice,
	})
	bin := filepath.Join(en.repo, ".ship", "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "pr-status"), []byte("#!/usr/bin/env bash\necho merged\n"), 0o755)
	gitRun(t, en.repo, "add", ".")
	gitRun(t, en.repo, "commit", "-qm", "pr-status")
	return en
}

// eventLines renders a run's events without ids and timestamps.
func eventLines(t *testing.T, st *store.Store, id string, skip map[string]bool) string {
	_, evs, err := store.Rebuild(st.RunDir(id))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range evs {
		if skip[e.Type] {
			continue
		}
		var d map[string]any
		json.Unmarshal(e.Data, &d)
		line := e.Type
		switch e.Type {
		case store.EvVisitStarted:
			line += fmt.Sprintf(" %v #%v", d["step"], d["visit_number"])
		case store.EvVisitFinished:
			line += fmt.Sprintf(" %v", d["outcome"])
		case store.EvTransition:
			line += fmt.Sprintf(" %v→%v (%v, %v)", d["from"], d["to"], d["outcome"], d["reason"])
		case store.EvStatusChanged:
			line += fmt.Sprintf(" %v", d["to"])
		case store.EvVarSet:
			line += fmt.Sprintf(" %v", d["name"])
		case store.EvChildStarted:
			line += fmt.Sprintf(" %v", d["slice_key"])
		case store.EvAskAnswered:
			line += fmt.Sprintf(" %v", d["choice"])
		case store.EvRunFinished:
			line += fmt.Sprintf(" %v", d["status"])
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", "e2e", name+".txt")
	if *updateGolden {
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(got), 0o644)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run go test -update): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s differs\n--- got\n%s--- want\n%s", name, got, want)
	}
}

func TestE2ESeriesStacked(t *testing.T) {
	en := newE2E(t, "series", true, "at-pass")
	s := en.start("feature", "---\ntitle: Rate limit\nvars: {ticket: API-1}\nacceptance: [limits work]\n---\nPlan.\n", nil, e2eScript(2))
	s = en.waitStatus(s.ID, store.StatusAsking)
	if s.PendingAsk == nil || s.PendingAsk.Kind != store.AskKindSplitReview || len(s.ProposedSlices) != 2 {
		t.Fatalf("split review: %+v %+v", s.PendingAsk, s.ProposedSlices)
	}
	slice1, _ := os.ReadFile(s.ProposedSlices[0].File)
	if !strings.Contains(string(slice1), "key: a") || !strings.Contains(string(slice1), "do a") {
		t.Fatalf("slice file:\n%s", slice1)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdSplitReview, Action: "resplit"}); KindOf(err) != KindInvalid {
		t.Fatalf("resplit without note should be invalid: %v", err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdSplitReview, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	s = en.waitFor(s.ID, "parent done", func(s *store.RunSnapshot) bool { return s.Status.Terminal() })
	if s.Status != store.StatusDone {
		t.Fatalf("parent %s (%s): %s", s.Status, s.StatusReason, visitTrail(s))
	}
	if len(s.Children) != 2 {
		t.Fatalf("children %+v", s.Children)
	}
	c1, _ := en.e.Snapshot(s.Children[0].ID)
	c2, _ := en.e.Snapshot(s.Children[1].ID)
	if c1.Branch != "API-1/1-a" || c2.Branch != "API-1/2-b" || c2.Base != "API-1/1-a" || c1.Base != "main" {
		t.Fatalf("branches %s←%s, %s←%s", c1.Branch, c1.Base, c2.Branch, c2.Base)
	}
	// Stacked: slice b's branch contains slice a's commit.
	gitRun(t, en.repo, "merge-base", "--is-ancestor", "API-1/1-a", "API-1/2-b")
	for _, c := range []*store.RunSnapshot{c1, c2, s} {
		if c.Workspace != nil {
			t.Errorf("%s: worktree not released", c.ID)
		}
	}
	if got := visitTrail(c1); got != "implement:done review:changes implement:done review:pass gate:pass at-pass:merged" {
		t.Errorf("child trail %q", got)
	}
	checkGolden(t, "series-parent", eventLines(t, en.st, s.ID, map[string]bool{store.EvChildFinished: true}))
	checkGolden(t, "series-child", eventLines(t, en.st, c1.ID, nil))
}

func TestE2EParallel(t *testing.T) {
	en := newE2E(t, "parallel", false, "done")
	// Track concurrency: count children running at once.
	maxRunning := 0
	unsub := en.e.subscribe(func(id string, ev store.Event) {
		if ev.Type != store.EvStatusChanged || !strings.Contains(id, ".") {
			return
		}
		n := 0
		for _, k := range []string{"01-a", "02-b", "03-c"} {
			parent := strings.SplitN(id, ".", 2)[0]
			if cs, err := en.st.Load(parent + "." + k); err == nil && !cs.Status.Terminal() {
				n++
			}
		}
		if n > maxRunning {
			maxRunning = n
		}
	})
	defer unsub()
	s := en.start("feature", "---\ntitle: Parallel\nvars: {ticket: API-2}\n---\n", nil, e2eScript(3))
	s = en.waitStatus(s.ID, store.StatusAsking)
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "approve"}); err != nil {
		t.Fatal(err)
	}
	s = en.waitFor(s.ID, "parent done", func(s *store.RunSnapshot) bool { return s.Status.Terminal() })
	if s.Status != store.StatusDone || len(s.Children) != 3 {
		t.Fatalf("%s %+v", s.Status, s.Children)
	}
	for _, c := range s.Children {
		cs, _ := en.e.Snapshot(c.ID)
		if cs.Base != "main" || cs.Status != store.StatusDone {
			t.Errorf("%s base %s status %s", c.ID, cs.Base, cs.Status)
		}
	}
	if maxRunning > 2 {
		t.Errorf("max_parallel exceeded: %d children ran at once", maxRunning)
	}
}

func TestE2EHaltOnChildStop(t *testing.T) {
	en := newE2E(t, "series", true, "at-pass")
	script := e2eScript(2) + "" // review "stuck" for every child → chef → abandon
	script = strings.Replace(script, "review:\n  - {outcome: changes, summary: \"missing tests\"}\n  - {outcome: pass, summary: lgtm}\n", "review:\n  - {outcome: stuck, summary: \"no idea\"}\n", 1)
	s := en.start("feature", "---\ntitle: Halt\nvars: {ticket: API-3}\n---\n", nil, script)
	en.waitStatus(s.ID, store.StatusAsking)
	en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "approve"})
	s = en.waitFor(s.ID, "child 1", func(s *store.RunSnapshot) bool { return len(s.Children) == 1 })
	child := s.Children[0].ID
	en.waitStatus(child, store.StatusAsking)
	if err := en.e.Do(child, Command{Name: CmdAnswer, Choice: "abandon"}); err != nil {
		t.Fatal(err)
	}
	s = en.waitFor(s.ID, "parent at chef", func(s *store.RunSnapshot) bool { return s.Status == store.StatusAsking && s.CurrentStep == "chef" })
	if len(s.Children) != 1 {
		t.Fatalf("halt should stop new children: %+v", s.Children)
	}
	if lv := s.Visits[len(s.Visits)-2]; lv.Step != "build" || lv.Outcome != "failed" {
		t.Fatalf("fanout visit %+v", lv)
	}
}

func TestSplitReviewReleasesAgentSlot(t *testing.T) {
	en := newE2E(t, "series", true, "at-pass")
	en.e.sem = make(chan struct{}, 1) // max_agents: 1
	s := en.start("feature", "---\ntitle: One\nvars: {ticket: API-9}\n---\n", nil, e2eScript(1))
	en.waitStatus(s.ID, store.StatusAsking)
	if n := en.e.Executing(); n != 0 {
		t.Fatalf("a run waiting for review shouldn't count as executing (got %d)", n)
	}
	// Another agent run must get the only slot while the review waits.
	other := newEnvSameEngine(en, map[string]string{"solo": "version: 1\nstart: a\nsteps:\n  a: {agent: /x, next: done}\n"})
	o := en.start(other, "", nil, "a: [{outcome: done, summary: ok}]\n")
	en.waitStatus(o.ID, store.StatusDone)
}

// newEnvSameEngine adds pipelines to the env's repo and returns the first name.
func newEnvSameEngine(en *env, pipelines map[string]string) string {
	var name string
	for n, src := range pipelines {
		writeFile(en.t, filepath.Join(en.repo, ".ship", "pipelines", n+".yml"), src)
		name = n
	}
	return name
}
