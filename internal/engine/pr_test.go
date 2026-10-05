package engine

import (
	"encoding/json"
	"fmt"
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
  "pr view") [ -f "$D/slow" ] && sleep 1; cat "$D/view.json" ;;
  "api --paginate") case "$3" in
      */issues/*) cat "$D/comments.json" 2>/dev/null || echo '[]' ;;
      *) cat "$D/inline.json" 2>/dev/null || echo '[]' ;;
    esac ;;
  "run view") cat "$D/log.txt" ;;
  "run rerun") echo "$3" >> "$D/reruns.log" ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`

type fakePR struct {
	dir     string
	t       *testing.T
	reviews []map[string]any
}

func newFakeGH(t *testing.T) *fakePR {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "gh"), []byte(fakeGHScript), 0o755)
	os.WriteFile(filepath.Join(dir, "log.txt"), []byte("step 1\nFAIL TestLimiter: boom\n"), 0o644)
	t.Setenv("SHIP_GH", filepath.Join(dir, "gh"))
	t.Setenv("FAKE_GH_DIR", dir)
	return &fakePR{dir: dir, t: t}
}

// set writes what gh reports: the PR view, its conversation comments and its
// inline comments (both in the REST API's shape).
func (f *fakePR) set(state, head string, checks []map[string]any, comments []map[string]any, inline []map[string]any) {
	if checks == nil {
		checks = []map[string]any{}
	}
	reviews := f.reviews
	if reviews == nil {
		reviews = []map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"number": 7, "url": "https://github.com/o/r/pull/7", "state": state, "headRefOid": head,
		"reviewDecision": "", "isDraft": false, "statusCheckRollup": checks, "reviews": reviews})
	os.WriteFile(filepath.Join(f.dir, "view.json"), b, 0o644)
	for name, list := range map[string][]map[string]any{"comments.json": comments, "inline.json": inline} {
		if list == nil {
			list = []map[string]any{}
		}
		lb, _ := json.Marshal(list)
		os.WriteFile(filepath.Join(f.dir, name), lb, 0o644)
	}
}

func check(name, status, conclusion, url string) map[string]any {
	return map[string]any{"__typename": "CheckRun", "name": name, "status": status, "conclusion": conclusion, "detailsUrl": url, "workflowName": "CI", "startedAt": time.Now().UTC().Format(time.RFC3339)}
}

func comment(id int, author, body string, ago time.Duration) map[string]any {
	user := map[string]string{"login": author, "type": "User"}
	if strings.HasSuffix(author, "[bot]") {
		user["type"] = "Bot"
	}
	return map[string]any{"id": id, "user": user, "body": body, "html_url": fmt.Sprintf("https://github.com/o/r/pull/7#c%d", id), "created_at": time.Now().Add(-ago).UTC().Format(time.RFC3339Nano)}
}

func review(author, state string, ago time.Duration) map[string]any {
	return map[string]any{"id": "rv-" + author + state, "author": map[string]string{"login": author}, "body": "", "state": state, "submittedAt": time.Now().Add(-ago).UTC().Format(time.RFC3339)}
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
	// Same head: CI isn't reported again. New comments arrive (a ship: note,
	// an agent reply and a status bot's report, none of which counts; a bot's
	// inline comment does). They're fresh, so settle covers the moment the
	// fake's files are half written.
	time.Sleep(200 * time.Millisecond)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	gh.set("OPEN", "aaa", nil, []map[string]any{
		comment(1, "alice", "Please handle the empty case.", 0),
		comment(2, "alice", "ship: the PR description was vague", 0),
		comment(3, "me", "Done. <!-- ship-agent -->", 0),
		comment(4, "codecov[bot]", "Coverage dropped by 0.2%", 0),
	}, []map[string]any{
		{"id": 99, "body": "Off by one here?", "html_url": "https://github.com/o/r/pull/7#d99", "path": "limiter.go", "line": 42, "user": map[string]string{"login": "bob", "type": "User"}, "created_at": now},
		{"id": 100, "body": "This can panic on nil.", "html_url": "https://github.com/o/r/pull/7#d100", "path": "limiter.go", "line": 50, "user": map[string]string{"login": "copilot[bot]", "type": "Bot"}, "created_at": now},
	})
	s = en.waitFor(s.ID, "feedback", func(s *store.RunSnapshot) bool { return len(s.Visits) >= 5 })
	if got := visitTrail(s); !strings.HasPrefix(got, "watch:ci_failed fixci:pass watch:feedback address:pass") {
		t.Fatal(got)
	}
	fb := s.Visits[2].Summary
	h := handover(en, s, 2)
	if !strings.Contains(fb, "3 new piece(s) of review feedback") || !strings.Contains(h, "handle the empty case") || !strings.Contains(h, "`limiter.go:42`") ||
		!strings.Contains(h, "can panic on nil") || strings.Contains(h, "PR description was vague") || strings.Contains(h, "Done.") || strings.Contains(h, "Coverage dropped") {
		t.Fatalf("feedback report:\n%s", h)
	}
	// Nothing new: the comments aren't sent twice. A cancelled check on a new
	// head is re-run (once for its Actions run, though two jobs were
	// cancelled), not reported.
	gh.set("OPEN", "bbb", []map[string]any{
		check("test", "COMPLETED", "CANCELLED", "https://github.com/o/r/actions/runs/77/job/2"),
		check("lint", "COMPLETED", "CANCELLED", "https://github.com/o/r/actions/runs/77/job/3"),
	}, nil, nil)
	en.waitFor(s.ID, "rerun", func(*store.RunSnapshot) bool {
		b, _ := os.ReadFile(filepath.Join(gh.dir, "reruns.log"))
		return strings.TrimSpace(string(b)) == "77"
	})
	time.Sleep(200 * time.Millisecond)
	if s2, _ := en.e.Snapshot(s.ID); len(s2.Visits) != 5 {
		t.Fatalf("nothing should fire: %s", visitTrail(s2))
	}
	if b, _ := os.ReadFile(filepath.Join(gh.dir, "reruns.log")); strings.TrimSpace(string(b)) != "77" {
		t.Fatalf("one re-run per Actions run, got %q", b)
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
	gh.set("OPEN", "aaa", nil, []map[string]any{comment(1, "alice", "Rename this.", time.Hour)}, nil)
	s := en.start("p", "", nil, "")
	s = en.waitFor(s.ID, "pending feedback", func(s *store.RunSnapshot) bool { return s.PR != nil && s.PR.PendingComments == 1 })
	time.Sleep(300 * time.Millisecond)
	if s2, _ := en.e.Snapshot(s.ID); len(s2.Visits) != 1 {
		t.Fatalf("manual mode shouldn't send feedback by itself: %s", visitTrail(s2))
	}
	// gh is slow, so the step is almost always mid-poll: the trigger is
	// queued for it rather than refused.
	os.WriteFile(filepath.Join(gh.dir, "slow"), nil, 0o644)
	time.Sleep(300 * time.Millisecond)
	if err := en.e.Do(s.ID, Command{Name: CmdPRTrigger}); err != nil {
		t.Fatal(err)
	}
	s = en.waitFor(s.ID, "feedback", func(s *store.RunSnapshot) bool { return len(s.Visits) >= 2 })
	if got := visitTrail(s); !strings.HasPrefix(got, "watch:feedback") {
		t.Fatal(got)
	}
}

func TestPRWatchReadyWithoutReviewDecision(t *testing.T) {
	gh := newFakeGH(t)
	en := newEnv(t, map[string]string{"p": strings.Replace(strings.Replace(prPipeline, "%s", "auto", 1), "merged: done", "merged: done, ready: done", 1)})
	green := []map[string]any{check("test", "COMPLETED", "SUCCESS", "")}
	// No branch protection, so GitHub reports no review decision: carol's
	// approval is outweighed by dave's request for changes (which is sent as
	// feedback).
	gh.reviews = []map[string]any{review("carol", "APPROVED", time.Hour), review("dave", "CHANGES_REQUESTED", 30*time.Minute)}
	gh.set("OPEN", "aaa", green, nil, nil)
	s := en.start("p", "", nil, "")
	s = en.waitFor(s.ID, "feedback", func(s *store.RunSnapshot) bool { return len(s.Visits) >= 3 })
	time.Sleep(300 * time.Millisecond)
	if s2, _ := en.e.Snapshot(s.ID); s2.Status == store.StatusDone {
		t.Fatalf("changes are requested, so it isn't ready: %s", visitTrail(s2))
	}
	// Dave approves; a later comment-only review doesn't undo that.
	gh.reviews = append(gh.reviews, review("dave", "APPROVED", time.Minute), review("dave", "COMMENTED", 0))
	gh.set("OPEN", "aaa", green, nil, nil)
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "watch:feedback address:pass watch:ready" {
		t.Fatal(got)
	}
}

func handover(en *env, s *store.RunSnapshot, i int) string {
	b, _ := os.ReadFile(filepath.Join(en.st.RunDir(s.ID), "visits", s.Visits[i].Dir, "handover.md"))
	return string(b)
}
