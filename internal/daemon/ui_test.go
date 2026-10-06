package daemon

import (
	"bytes"
	"html/template"
	"net/url"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// The run's PR stays linked from its header once the pr step has moved on
// (and after the run finishes), not only while the step watches it.
func TestRunHeaderLinksPR(t *testing.T) {
	tp, err := tpl.get("run")
	if err != nil {
		t.Fatal(err)
	}
	s := &store.RunSnapshot{ID: "r1", Title: "t", Status: store.StatusDone, Branch: "ship/r1", Base: "main",
		PR: &store.PRStatus{Step: "pr", URL: "https://github.com/o/r/pull/7", Number: 7, State: "MERGED"}}
	var buf bytes.Buffer
	if err := tp.ExecuteTemplate(&buf, "run-header", &RunView{S: s}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `href="https://github.com/o/r/pull/7"`) || !strings.Contains(buf.String(), "PR #7") {
		t.Fatalf("header should link the PR:\n%s", buf.String())
	}
	s.PR = &store.PRStatus{Step: "pr", Note: "no PR for branch ship/r1 yet"}
	buf.Reset()
	tp.ExecuteTemplate(&buf, "run-header", &RunView{S: s})
	if strings.Contains(buf.String(), "PR #") {
		t.Fatalf("no link without a PR:\n%s", buf.String())
	}
}

// A repo pipeline's page asks for its graph with the repo intact, not
// escaped a second time (repo%3d…), which lost the repo and broke the graph.
func TestPipelinePageGraphQuery(t *testing.T) {
	tp, err := tpl.get("pipeline")
	if err != nil {
		t.Fatal(err)
	}
	v := &PipelinePage{Name: "release", Repo: "/a b/repo", Query: template.URL(url.Values{"repo": {"/a b/repo"}}.Encode()), HasGraph: true, Scope: "done"}
	var buf bytes.Buffer
	if err := tp.ExecuteTemplate(&buf, "content", layoutData{Data: v}); err != nil {
		t.Fatal(err)
	}
	if want := `data-src="/api/pipelines/release/graph?repo=%2Fa&#43;b%2Frepo"`; !strings.Contains(buf.String(), want) {
		t.Fatalf("want %s in:\n%s", want, buf.String())
	}
}

// A fanout parent lists every slice: started ones link to their runs,
// pending ones offer Start now (disabled, with the reason, when blocked).
func TestRunSideListsPendingSlices(t *testing.T) {
	tp, err := tpl.get("run")
	if err != nil {
		t.Fatal(err)
	}
	s := &store.RunSnapshot{ID: "p1", Title: "t", Status: store.StatusFannedOut}
	v := &RunView{S: s, SliceRows: []engine.SliceState{
		{Number: 1, Title: "One", RunID: "p1.01-a", Status: store.StatusRunning, Step: "implement"},
		{Number: 2, Title: "Two", Status: engine.SlicePending, CanStart: true},
		{Number: 3, Title: "Three", Status: engine.SlicePending, Why: "slice 3 stacks on slice 2, which hasn't started yet"},
	}}
	var buf bytes.Buffer
	if err := tp.ExecuteTemplate(&buf, "run-side", v); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`href="/runs/p1.01-a"`, "1. One", "2. Two", "3. Three", `{"slice":"2"}`, "disabled title=\"slice 3 stacks on slice 2, which hasn&#39;t started yet\""} {
		if !strings.Contains(out, want) {
			t.Fatalf("want %s in:\n%s", want, out)
		}
	}
	if strings.Count(out, "/start-slice") != 2 {
		t.Fatalf("Start now on each pending slice:\n%s", out)
	}
}
