// Package none runs in the main checkout directly, with no branch creation.
package none

import (
	"context"
	"os"

	"github.com/thehenrymcintosh/ship/internal/workspace"
	"github.com/thehenrymcintosh/ship/internal/workspace/git"
)

// Provider is the `none` provider.
type Provider struct{}

// Name implements workspace.Provider.
func (Provider) Name() string { return "none" }

// Check verifies the repo dir exists.
func (Provider) Check(ctx context.Context, repo string) error {
	_, err := os.Stat(repo)
	return err
}

// Acquire returns the main checkout as the workspace.
func (Provider) Acquire(ctx context.Context, req workspace.AcquireRequest) (workspace.Lease, error) {
	branch := git.CurrentBranch(ctx, req.Repo)
	return workspace.Lease{
		Provider: "none", Path: req.Repo, Branch: branch, Base: req.Base,
		BaseSHA: git.Tip(ctx, req.Repo, req.Base),
	}, nil
}

// Release does nothing: the main checkout is never removed.
func (Provider) Release(context.Context, workspace.Lease, workspace.ReleaseOpts) error { return nil }

// Exists reports whether the checkout is there.
func (Provider) Exists(l workspace.Lease) bool {
	_, err := os.Stat(l.Path)
	return err == nil
}
