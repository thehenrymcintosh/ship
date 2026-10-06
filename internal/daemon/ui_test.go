package daemon

import (
	"bytes"
	"strings"
	"testing"

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
