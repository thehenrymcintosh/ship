package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// fakeGH answers the three gh calls the poller makes.
const fakeGH = `#!/bin/sh
case "$*" in
  "pr list --head ship/r1 "*) echo '[{"number":7,"url":"https://github.com/o/r/pull/7"}]' ;;
  "pr list "*) echo '[]' ;;
  "pr view 7 "*) cat <<'EOF'
{"url":"https://github.com/o/r/pull/7",
 "comments":[{"id":"c1","author":{"login":"henry"},"body":"ship: the PR description never says why","url":"https://github.com/o/r/pull/7#c1"},
             {"id":"c2","author":{"login":"henry"},"body":"please rename this variable","url":"https://github.com/o/r/pull/7#c2"}],
 "reviews":[{"id":"r1","author":{"login":"sam"},"body":"SHIP: tests only cover the happy path"},{"id":"r2","author":{"login":"sam"},"body":""}]}
EOF
  ;;
  "api repos/{owner}/{repo}/pulls/7/comments") cat <<'EOF'
[{"id":99,"body":"ship:\n  error handling is copy-pasted","html_url":"https://github.com/o/r/pull/7#d99","user":{"login":"henry"},"path":"a.go"}]
EOF
  ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`

func TestPRFeedbackImport(t *testing.T) {
	root := t.TempDir()
	gh := filepath.Join(root, "gh")
	os.WriteFile(gh, []byte(fakeGH), 0o755)
	st, _ := store.New(filepath.Join(root, "state"))
	histDir := filepath.Join(root, "repo", ".ship", "history", "pr")
	for _, run := range []struct{ id, branch string }{{"r1", "ship/r1"}, {"r2", "ship/r2"}} {
		rl, err := st.Create(run.id)
		if err != nil {
			t.Fatal(err)
		}
		rl.Emit(store.EvRunCreated, store.ActorEngine, store.RunCreated{ID: run.id, Pipeline: "pr", Repo: root, Branch: run.branch, Provider: "git", PipelineVersion: 2, PipelineHash: "abc", HistoryDir: histDir})
		rl.Close()
	}
	p := newPRPoller(st, filepath.Join(root, "home"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.gh = gh
	if n := p.once(context.Background()); n != 3 {
		t.Fatalf("want 3 pieces of feedback, got %d", n)
	}
	// A second poll imports nothing new.
	if n := p.once(context.Background()); n != 0 {
		t.Fatalf("re-import: %d", n)
	}
	items, _ := history.Open(histDir, filepath.Join(root, "locks")).Items()
	var got []string
	for _, it := range items {
		if it.Run != "r1" || it.Version != 2 || it.Source != history.FromPR || it.Link == "" {
			t.Errorf("%+v", it)
		}
		got = append(got, it.Author+": "+it.Text)
	}
	want := "henry: the PR description never says why|sam: tests only cover the happy path|henry: error handling is copy-pasted"
	if strings.Join(got, "|") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
}
