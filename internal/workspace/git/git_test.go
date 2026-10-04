package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/workspace"
)

// NewTestRepo creates a repo with one commit on main.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(dir, 0o755)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	return dir
}

func TestAcquireStackRelease(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	p := &Provider{DirTemplate: "{repo_parent}/{repo}.ship/{run}", Fetch: true}

	if got := DefaultBranch(ctx, repo); got != "main" {
		t.Fatalf("default branch %q", got)
	}
	l1, err := p.Acquire(ctx, workspace.AcquireRequest{Repo: repo, RunID: "r1", Branch: "feat/1", Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if l1.Path != filepath.Join(filepath.Dir(repo), "repo.ship", "r1") || CurrentBranch(ctx, l1.Path) != "feat/1" || l1.BaseSHA == "" {
		t.Fatalf("%+v", l1)
	}
	if mc, _ := MainCheckout(ctx, l1.Path); mc != repo {
		// macOS temp dirs resolve through /private.
		if r, _ := filepath.EvalSymlinks(repo); mc != r {
			t.Fatalf("main checkout %q", mc)
		}
	}
	os.WriteFile(filepath.Join(l1.Path, "a.txt"), []byte("a"), 0o644)
	if _, err := Git(ctx, l1.Path, "add", "."); err != nil {
		t.Fatal(err)
	}
	Git(ctx, l1.Path, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-qm", "a")

	// Stacked: child 2 branches from child 1's branch, visible without a push.
	l2, err := p.Acquire(ctx, workspace.AcquireRequest{Repo: repo, RunID: "r2", Branch: "feat/2", Base: "feat/1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l2.Path, "a.txt")); err != nil {
		t.Fatal("stacked worktree should contain parent's commit")
	}
	// Idempotent re-acquire.
	again, err := p.Acquire(ctx, workspace.AcquireRequest{Repo: repo, RunID: "r2", Branch: "feat/2", Base: "feat/1"})
	if err != nil || again.Path != l2.Path {
		t.Fatal(again, err)
	}
	if err := p.Release(ctx, l2, workspace.ReleaseOpts{}); err != nil {
		t.Fatal(err)
	}
	if p.Exists(l2) || !HasLocalBranch(ctx, repo, "feat/2") {
		t.Fatal("release should remove the tree and keep the branch")
	}
	// Re-acquire on an existing branch checks it out without resetting it.
	l2b, err := p.Acquire(ctx, workspace.AcquireRequest{Repo: repo, RunID: "r2", Branch: "feat/2", Base: "feat/1"})
	if err != nil || CurrentBranch(ctx, l2b.Path) != "feat/2" {
		t.Fatal(l2b, err)
	}
	// Dirty tree needs force.
	os.WriteFile(filepath.Join(l2b.Path, "dirty"), []byte("x"), 0o644)
	if err := p.Release(ctx, l2b, workspace.ReleaseOpts{}); err == nil {
		t.Fatal("dirty release without force should fail")
	}
	if err := p.Release(ctx, l2b, workspace.ReleaseOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestSetupCommands(t *testing.T) {
	repo := newTestRepo(t)
	p := &Provider{DirTemplate: "{repo_parent}/wt/{run}", Setup: []string{"echo ok > setup.txt"}}
	l, err := p.Acquire(context.Background(), workspace.AcquireRequest{Repo: repo, RunID: "r", Branch: "b", Base: "main", Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(l.Path, "setup.txt")); string(b) != "ok\n" {
		t.Fatal("setup didn't run")
	}
	p.Setup = []string{"exit 3"}
	if _, err := p.Acquire(context.Background(), workspace.AcquireRequest{Repo: repo, RunID: "r2", Branch: "c", Base: "main", Env: os.Environ()}); err == nil {
		t.Fatal("failing setup should fail acquire")
	}
}
