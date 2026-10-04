// Package git is the default workspace provider: one `git worktree` per run
// . It also holds git helpers shared by other packages.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thehenrymcintosh/ship/internal/proc"
	"github.com/thehenrymcintosh/ship/internal/workspace"
)

// Git runs git in dir and returns trimmed stdout.
func Git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// MainCheckout maps any path inside a repo (or one of its worktrees) to the
// main checkout.
func MainCheckout(ctx context.Context, path string) (string, error) {
	common, err := Git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", path)
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	// A bare repo or unusual layout: fall back to the toplevel.
	return Git(ctx, path, "rev-parse", "--show-toplevel")
}

// DefaultBranch returns origin's HEAD branch, falling back to the main
// checkout's current branch.
func DefaultBranch(ctx context.Context, repo string) string {
	if ref, err := Git(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(ref, "refs/remotes/origin/")
	}
	if b, err := Git(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && b != "HEAD" {
		return b
	}
	return "main"
}

// CurrentBranch returns the checked-out branch of dir.
func CurrentBranch(ctx context.Context, dir string) string {
	b, _ := Git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	return b
}

// Origin returns the origin remote URL, or "".
func Origin(ctx context.Context, repo string) string {
	u, _ := Git(ctx, repo, "remote", "get-url", "origin")
	return u
}

// HasLocalBranch reports whether refs/heads/<b> exists.
func HasLocalBranch(ctx context.Context, repo, b string) bool {
	_, err := Git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+b)
	return err == nil
}

func hasRemoteBranch(ctx context.Context, repo, b string) bool {
	_, err := Git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b)
	return err == nil
}

// Tip returns the SHA of a ref, or "".
func Tip(ctx context.Context, repo, ref string) string {
	sha, _ := Git(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return sha
}

// ResolveBase picks the ref to branch from: the local branch, unless origin
// has it and the local one is missing or behind. With fetch, origin is
// fetched first when it has the branch.
func ResolveBase(ctx context.Context, repo, base string, fetch bool) string {
	if fetch && hasRemote(ctx, repo) && (hasRemoteBranch(ctx, repo, base) || !HasLocalBranch(ctx, repo, base)) {
		_, _ = Git(ctx, repo, "fetch", "--quiet", "origin", base)
	}
	local := HasLocalBranch(ctx, repo, base)
	remote := hasRemoteBranch(ctx, repo, base)
	switch {
	case local && remote:
		// Use origin when local is an ancestor of it (behind or equal).
		if _, err := Git(ctx, repo, "merge-base", "--is-ancestor", "refs/heads/"+base, "refs/remotes/origin/"+base); err == nil {
			return "origin/" + base
		}
		return base
	case remote:
		return "origin/" + base
	}
	return base
}

func hasRemote(ctx context.Context, repo string) bool { return Origin(ctx, repo) != "" }

var slugRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ExpandDir fills the worktree dir template: {repo} {repo_parent} {run}
// {branch_slug}.
func ExpandDir(tmpl, repo, run, branch string) string {
	r := strings.NewReplacer(
		"{repo}", filepath.Base(repo),
		"{repo_parent}", filepath.Dir(repo),
		"{run}", run,
		"{branch_slug}", slugRE.ReplaceAllString(branch, "-"),
	)
	p := r.Replace(tmpl)
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	return filepath.Clean(p)
}

// Provider is the git worktree provider.
type Provider struct {
	DirTemplate string
	Fetch       bool
	Setup       []string
}

// Name implements workspace.Provider.
func (*Provider) Name() string { return "git" }

// Check verifies git and the repo.
func (*Provider) Check(ctx context.Context, repo string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git is not installed")
	}
	_, err := Git(ctx, repo, "rev-parse", "--git-dir")
	return err
}

// Acquire creates (or re-attaches) the run's worktree. It is idempotent: an
// existing worktree at the path on the right branch is reused, and an
// existing branch is checked out rather than created or reset.
func (p *Provider) Acquire(ctx context.Context, req workspace.AcquireRequest) (workspace.Lease, error) {
	path := ExpandDir(p.DirTemplate, req.Repo, req.RunID, req.Branch)
	lease := workspace.Lease{Provider: "git", Path: path, Branch: req.Branch, Base: req.Base, Meta: map[string]string{"repo": req.Repo}}

	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		if b := CurrentBranch(ctx, path); b == req.Branch {
			lease.BaseSHA = Tip(ctx, req.Repo, req.Base)
			return lease, nil
		}
		return lease, fmt.Errorf("worktree path %s already exists and isn't on branch %s", path, req.Branch)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return lease, err
	}
	// A stale registration (dir deleted by hand) blocks `worktree add`.
	_, _ = Git(ctx, req.Repo, "worktree", "prune")
	if HasLocalBranch(ctx, req.Repo, req.Branch) {
		if _, err := Git(ctx, req.Repo, "worktree", "add", path, req.Branch); err != nil {
			return lease, err
		}
		lease.BaseSHA = Tip(ctx, req.Repo, req.Base)
	} else {
		ref := ResolveBase(ctx, req.Repo, req.Base, p.Fetch)
		lease.BaseSHA = Tip(ctx, req.Repo, ref)
		if lease.BaseSHA == "" {
			return lease, fmt.Errorf("base %q doesn't exist in %s", req.Base, req.Repo)
		}
		if _, err := Git(ctx, req.Repo, "worktree", "add", "--no-track", "-b", req.Branch, path, ref); err != nil {
			return lease, err
		}
	}
	for _, c := range p.Setup {
		var out bytes.Buffer
		res, err := proc.Run(ctx, proc.Spec{Path: "bash", Args: []string{"-euo", "pipefail", "-c", c}, Dir: path, Env: req.Env, Stdout: &out, Stderr: &out})
		if err != nil || res.ExitCode != 0 {
			return lease, fmt.Errorf("workspace setup %q failed (exit %d): %s", c, res.ExitCode, strings.TrimSpace(out.String()))
		}
	}
	return lease, nil
}

// Release removes the worktree (keeping the branch) and prunes.
func (p *Provider) Release(ctx context.Context, l workspace.Lease, opts workspace.ReleaseOpts) error {
	repo := l.Meta["repo"]
	if _, statErr := os.Stat(l.Path); os.IsNotExist(statErr) {
		// Already gone: just drop the stale registration.
		if repo != "" {
			_, _ = Git(ctx, repo, "worktree", "prune")
		}
		return nil
	}
	if repo == "" {
		var err error
		if repo, err = MainCheckout(ctx, l.Path); err != nil {
			return err
		}
	}
	args := []string{"worktree", "remove"}
	if opts.Force {
		args = append(args, "--force")
	}
	if _, err := Git(ctx, repo, append(args, l.Path)...); err != nil {
		return err
	}
	_, _ = Git(ctx, repo, "worktree", "prune")
	return nil
}

// Exists reports whether the worktree is still there.
func (*Provider) Exists(l workspace.Lease) bool {
	fi, err := os.Stat(l.Path)
	return err == nil && fi.IsDir()
}
