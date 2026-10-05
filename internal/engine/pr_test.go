package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
)

const fakeGHScript = `#!/bin/sh
D="$FAKE_GH_DIR"
case "$1 $2" in
  "pr view") cat "$D/view.json" ;;
  "api --paginate") cat "$D/inline.json" 2>/dev/null || echo '[]' ;;
  "run view") cat "$D/log.txt" ;;
  "run rerun") echo "$3" >> "$D/reruns.log" ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`

type fakePR struct {
	dir string
	t   *testing.T
}

func newFakeGH(t *testing.T) *fakePR {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "gh"), []byte(fakeGHScript), 0o755)
	os.WriteFile(filepath.Join(dir, "log.txt"), []byte("step 1\nFAIL TestLimiter: boom\n"), 0o644)
	t.Setenv("SHIP_GH", filepath.Join(dir, "gh"))
	t.Setenv("FAKE_GH_DIR", dir)
	return &fakePR{dir: dir, t: t}
}

func (f *fakePR) set(state, head string, checks []map[string]any, comments []map[string]any, inline []map[string]any) {
	if checks == nil {
		checks = []map[string]any{}
	}
	if comments == nil {
		comments = []map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"number": 7, "url": "https://github.com/o/r/pull/7", "state": state, "headRefOid": head,
		"reviewDecision": "", "isDraft": false, "statusCheckRollup": checks, "comments": comments, "reviews": []any{}})
	os.WriteFile(filepath.Join(f.dir, "view.json"), b, 0o644)
	if inline == nil {
		inline = []map[string]any{}
	}
	ib, _ := json.Marshal(inline)
	os.WriteFile(filepath.Join(f.dir, "inline.json"), ib, 0o644)
}

func check(name, status, conclusion, url string) map[string]any {
	return map[string]any{"__typename": "CheckRun", "name": name, "status": status, "conclusion": conclusion, "detailsUrl": url, "workflowName": "CI", "startedAt": time.Now().UTC().Format(time.RFC3339)}
}

func comment(id, author, body string, ago time.Duration) map[string]any {
	return map[string]any{"id": id, "author": map[string]string{"login": author}, "body": body, "url": "https://github.com/o/r/pull/7#" + id, "createdAt": time.Now().Add(-ago).UTC().Format(time.RFC3339)}
}

const prPipeline = `version: 1
start: watch
workspace: {provider: none}
defaults: {max_visits: 0}
steps:
  watch:
    pr: ""
    every: 50ms
    settle: 300ms
    trigger: %s
    next: {feedback: address, ci_failed: fixci, merged: done, closed: stop}
  address: {run: "echo addressed", next: watch}
  fixci: {run: "echo fixed", next: watch}
`

func TestPRWatchAuto(t *testing.T) {
	gh := newFakeGH(t)
	en := newEnv(t, map[string]string{"p": strings.Replace(prPipeline, "%s", "auto", 1)})
	// A failure while another check is still running: not settled, so nothing fires yet.
	gh.set("OPEN", "aaa", []map[string]any{
		check("test", "COMPLETED", "FAILURE", "https://github.com/o/r/actions/runs/11/job/1"),
		check("lint", "IN_PROGRESS", "", ""),
	}, nil, nil)
	s := en.start("p", "", nil, "")
	s = en.waitFor(s.ID, "PR observed", func(s *store.RunSnapshot) bool { return s.PR != nil && s.PR.ChecksPending == 1 })
	if len(s.Visits) != 1 || s.Status != store.StatusWaiting {
		t.Fatalf("should keep waiting while checks run: %s", visitTrail(s))
	}
	// Settled with a failure: ci_failed fires once, with the failing log.
	gh.set("OPEN", "aaa", []map[string]any{
		check("test", "COMPLETED", "FAILURE", "https://github.com/o/r/actions/runs/11/job/1"),
		check("lint", "COMPLETED", "SUCCESS", ""),
	}, nil, nil)
	s = en.waitFor(s.ID, "ci_failed", func(s *store.RunSnapshot) bool { return len(s.Visits) >= 3 })
	if got := visitTrail(s); !strings.HasPrefix(got, "watch:ci_failed fixci:pass") {
		t.Fatal(got)
	}
	if !strings.Contains(s.Visits[0].Summary, "CI / test") || !strings.Contains(handover(en, s, 0), "FAIL TestLimiter: boom") {
		t.Fatalf("ci report:\n%s", handover(en, s, 0))
	}
	// Same head: CI isn't reported again. New comments arrive (one is a
	// ship: note and one an agent reply, neither of which counts).
	time.Sleep(200 * time.Millisecond)
	gh.set("OPEN", "aaa", nil, []map[string]any{
		comment("c1", "alice", "Please handle the empty case.", time.Second),
		comment("c2", "alice", "ship: the PR description was vague", time.Second),
		comment("c3", "me", "Done. <!-- ship-agent -->", time.Second),
	}, []map[string]any{{"id": 99, "body": "Off by one here?", "html_url": "https://github.com/o/r/pull/7#d99", "path": "limiter.go", "line": 42, "user": map[string]string{"login": "bob"}, "created_at": time.Now().Add(-time.Second).UTC().Format(time.RFC3339)}})
	s = en.waitFor(s.ID, "feedback", func(s *store.RunSnapshot) bool { return len(s.Visits) >= 5 })
	if got := visitTrail(s); !strings.HasPrefix(got, "watch:ci_failed fixci:pass watch:feedback address:pass") {
		t.Fatal(got)
	}
	fb := s.Visits[2].Summary
	h := handover(en, s, 2)
	if !strings.Contains(fb, "2 new piece(s) of review feedback") || !strings.Contains(h, "handle the empty case") || !strings.Contains(h, "`limiter.go:42`") || strings.Contains(h, "PR description was vague") || strings.Contains(h, "Done.") {
		t.Fatalf("feedback report:\n%s", h)
	}
	// Nothing new: the comments aren't sent twice. A cancelled check on a new
	// head is re-run, not reported.
	gh.set("OPEN", "bbb", []map[string]any{check("test", "COMPLETED", "CANCELLED", "https://github.com/o/r/actions/runs/77/job/2")}, nil, nil)
	en.waitFor(s.ID, "rerun", func(*store.RunSnapshot) bool {
		b, _ := os.ReadFile(filepath.Join(gh.dir, "reruns.log"))
		return strings.TrimSpace(string(b)) == "77"
	})
	time.Sleep(200 * time.Millisecond)
	if s2, _ := en.e.Snapshot(s.ID); len(s2.Visits) != 5 {
		t.Fatalf("nothing should fire: %s", visitTrail(s2))
	}
	gh.set("MERGED", "bbb", nil, nil, nil)
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); !strings.HasSuffix(got, "watch:merged") {
		t.Fatal(got)
	}
}

func TestPRWatchManualTrigger(t *testing.T) {
	gh := newFakeGH(t)
	en := newEnv(t, map[string]string{"p": strings.Replace(prPipeline, "%s", "manual", 1)})
	gh.set("OPEN", "aaa", nil, []map[string]any{comment("c1", "alice", "Rename this.", time.Hour)}, nil)
	s := en.start("p", "", nil, "")
	s = en.waitFor(s.ID, "pending feedback", func(s *store.RunSnapshot) bool { return s.PR != nil && s.PR.PendingComments == 1 })
	time.Sleep(300 * time.Millisecond)
	if s2, _ := en.e.Snapshot(s.ID); len(s2.Visits) != 1 {
		t.Fatalf("manual mode shouldn't send feedback by itself: %s", visitTrail(s2))
	}
	if err := en.e.Do(s.ID, Command{Name: CmdPRTrigger}); err != nil {
		t.Fatal(err)
	}
	s = en.waitFor(s.ID, "feedback", func(s *store.RunSnapshot) bool { return len(s.Visits) >= 2 })
	if got := visitTrail(s); !strings.HasPrefix(got, "watch:feedback") {
		t.Fatal(got)
	}
}

func handover(en *env, s *store.RunSnapshot, i int) string {
	b, _ := os.ReadFile(filepath.Join(en.st.RunDir(s.ID), "visits", s.Visits[i].Dir, "handover.md"))
	return string(b)
}
