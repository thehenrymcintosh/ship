package treehouse

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/workspace"
)

// These tests run against the real treehouse CLI when it's on PATH, with
// the pool in a temp dir (TREEHOUSE_ROOT) so nothing touches ~/.treehouse.
func setup(t *testing.T) (repo string, p *Provider) {
	t.Helper()
	if _, err := exec.LookPath("treehouse"); err != nil {
		t.Skip("treehouse not installed")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("TREEHOUSE_ROOT", filepath.Join(root, "pool"))
	repo = filepath.Join(root, "repo")
	os.MkdirAll(repo, 0o755)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@e"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	return repo, &Provider{}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestLeaseWorkReturn(t *testing.T) {
	repo, p := setup(t)
	ctx := context.Background()
	req := workspace.AcquireRequest{Repo: repo, RunID: "run1", Branch: "ship/run1", Base: "main", Env: os.Environ()}
	l, err := p.Acquire(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if l.LeaseID == "" || !p.Exists(l) || gitOut(t, l.Path, "rev-parse", "--abbrev-ref", "HEAD") != "ship/run1" {
		t.Fatalf("lease %+v", l)
	}
	// treehouse shows ship as the holder.
	list, err := p.status(ctx, repo)
	if err != nil || len(list) != 1 || list[0].LeaseHolder != "ship:run1" {
		t.Fatalf("status %+v %v", list, err)
	}
	// Acquire is idempotent for the same run (crash recovery).
	again, err := p.Acquire(ctx, req)
	if err != nil || again.Path != l.Path || again.LeaseID != l.LeaseID {
		t.Fatalf("re-acquire %+v %v", again, err)
	}
	// Uncommitted work blocks a plain release; committed work survives one.
	os.WriteFile(filepath.Join(l.Path, "work.txt"), []byte("x"), 0o644)
	if err := p.Release(ctx, l, workspace.ReleaseOpts{}); err == nil {
		t.Fatal("releasing a dirty worktree should fail without force")
	}
	gitOut(t, l.Path, "add", "-A")
	gitOut(t, l.Path, "commit", "-qm", "work")
	if err := p.Release(ctx, l, workspace.ReleaseOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gitOut(t, repo, "log", "--oneline", "ship/run1"), "work") {
		t.Fatal("commit lost after return")
	}
	if p.Exists(l) {
		t.Fatal("lease should be gone after return")
	}
	// Re-acquire on the existing branch (worktree lost, branch kept).
	l2, err := p.Acquire(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if gitOut(t, l2.Path, "rev-parse", "--abbrev-ref", "HEAD") != "ship/run1" {
		t.Fatal("re-acquired worktree should be on the run's branch")
	}
	if _, err := os.Stat(filepath.Join(l2.Path, "work.txt")); err != nil {
		t.Fatal("re-acquired worktree should have the branch's commits")
	}
	if err := p.Release(ctx, l2, workspace.ReleaseOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
}
