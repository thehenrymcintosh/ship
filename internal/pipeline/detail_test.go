package pipeline

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Graph nodes carry what each step does, for the UI's step panel.
func TestGraphStepDetails(t *testing.T) {
	f, fs := Parse("detail.yml", []byte(`version: 1
start: build
defaults:
  on_error: check-in
steps:
  build:
    description: Build it
    agent: /implement
    prompt: "Build <b>it</b>."
    model: opus
    effort: high
    session: continue
    permission_mode: acceptEdits
    save: [summary]
    max_budget_usd: 2.5
    max_tokens: 200k
    max_visits: 3
    when_exhausted: check-in
    next: {done: test}
  test:
    run: make test
    outcomes: {0: pass, 3: flaky}
    timeout: 5m
    next: {pass: done, fail: build, flaky: test}
  check-in:
    ask: "Stopped at {{came_from}}. What next?"
    show: [prev.handover]
    choices: {retry: build, abandon: stop}
`))
	if f == nil {
		t.Fatal(fs)
	}
	raw, err := json.Marshal(f.Pipeline.Graph())
	if err != nil {
		t.Fatal(err)
	}
	var g Graph
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	details := map[string]*StepDetail{}
	for _, n := range g.Nodes {
		details[n.ID] = n.Detail
	}
	if details[TargetDone] != nil {
		t.Error("done shouldn't have a detail")
	}

	b := details["build"]
	if b == nil || b.Agent != "/implement" || b.Prompt != "Build <b>it</b>." || b.Model != "opus" || b.Effort != "high" ||
		b.Session != "continue" || b.PermissionMode != "acceptEdits" || b.MaxBudgetUSD != 2.5 || b.MaxTokens != 200000 ||
		b.MaxVisits != 3 || b.WhenExhausted != "check-in" || b.OnError != "check-in" || b.Timeout != "30m0s" {
		t.Errorf("agent detail: %+v", b)
	}
	if !reflect.DeepEqual(b.Save, []Pair{{Key: "summary"}}) {
		t.Errorf("agent save: %+v", b.Save)
	}
	wantRoutes := []Route{{"done", "test", "outcome"}, {"error", "check-in", "error"}, {"exhausted", "check-in", "exhausted"}}
	if !reflect.DeepEqual(b.Routes, wantRoutes) {
		t.Errorf("agent routes: %+v", b.Routes)
	}

	r := details["test"]
	if r == nil || r.Run != "make test" || r.Timeout != "5m0s" || r.Prompt != "" ||
		!reflect.DeepEqual(r.ExitCode, []Pair{{"0", "pass"}, {"3", "flaky"}}) {
		t.Errorf("run detail: %+v", r)
	}
	wantRoutes = []Route{{"pass", "done", "outcome"}, {"flaky", "test", "outcome"}, {"fail", "build", "outcome"},
		{"error", "check-in", "error"}}
	if !reflect.DeepEqual(r.Routes, wantRoutes) {
		t.Errorf("run routes: %+v", r.Routes)
	}

	a := details["check-in"]
	if a == nil || a.Ask != "Stopped at {{came_from}}. What next?" || !reflect.DeepEqual(a.Show, []string{"prev.handover"}) || a.Timeout != "" {
		t.Errorf("ask detail: %+v", a)
	}
	if len(a.Routes) < 2 || a.Routes[0] != (Route{"retry", "build", "outcome"}) || a.Routes[1] != (Route{"abandon", "stop", "outcome"}) {
		t.Errorf("ask routes: %+v", a.Routes)
	}
}
