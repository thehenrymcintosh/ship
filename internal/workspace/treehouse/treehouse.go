// Package treehouse is the opt-in workspace provider backed by
// https://github.com/kunchenguid/treehouse. ship defers to
// treehouse's own config for placement, pooling, cloning and hooks.
package treehouse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/merlin-digital/ship/internal/brand"
	"github.com/merlin-digital/ship/internal/workspace"
	"github.com/merlin-digital/ship/internal/workspace/git"
)

// Provider is the treehouse provider.
type Provider struct {
	// Binary is the CLI (default "treehouse").
	Binary string
}

// Name implements workspace.Provider.
func (*Provider) Name() string { return "treehouse" }

func (p *Provider) bin() string {
	if p.Binary == "" {
		return "treehouse"
	}
	return p.Binary
}

// ErrNotInstalled is returned by Check when treehouse isn't on PATH. There's
// no silent fallback to git.
var ErrNotInstalled = errors.New(`workspace provider "treehouse" selected but treehouse is not installed — install it or set workspace.provider: git`)

// Check requires treehouse on PATH.
func (p *Provider) Check(ctx context.Context, repo string) error {
	if _, err := exec.LookPath(p.bin()); err != nil {
		return ErrNotInstalled
	}
	return nil
}

func (p *Provider) run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.bin(), args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("treehouse %s: %s", strings.Join(args, " "), msg)
	}
	return out.Bytes(), nil
}

type getResult struct {
	Path        string `json:"path"`
	LeaseID     string `json:"lease_id"`
	LeaseHolder string `json:"lease_holder"`
	LeasedAt    string `json:"leased_at"`
	BaseBranch  string `json:"base_branch"`
}

// Holder is the lease holder string for a run.
func Holder(runID string) string { return brand.LeaseHolderPrefix + runID }

// Acquire leases a tree. A new branch is created with -b; an existing branch
// (re-acquire) is leased on its own base and switched to.
func (p *Provider) Acquire(ctx context.Context, req workspace.AcquireRequest) (workspace.Lease, error) {
	lease := workspace.Lease{Provider: "treehouse", Branch: req.Branch, Base: req.Base, Meta: map[string]string{"repo": req.Repo}}
	if existing, ok := p.existingLease(ctx, req); ok {
		existing.Branch, existing.Base, existing.Meta = req.Branch, req.Base, lease.Meta
		existing.BaseSHA = git.Tip(ctx, req.Repo, req.Base)
		return existing, nil
	}
	branchExists := git.HasLocalBranch(ctx, req.Repo, req.Branch)
	var args []string
	if branchExists {
		args = []string{"get", "--lease", "--json", "--base", req.Branch, "--lease-holder", Holder(req.RunID)}
	} else {
		args = []string{"get", "--lease", "--json", "-b", req.Branch, "--base", req.Base, "--lease-holder", Holder(req.RunID)}
	}
	out, err := p.run(ctx, req.Repo, req.Env, args...)
	if err != nil {
		return lease, err
	}
	var g getResult
	if err := json.Unmarshal(lastJSON(out), &g); err != nil || g.Path == "" {
		return lease, fmt.Errorf("treehouse get: unexpected output %q", strings.TrimSpace(string(out)))
	}
	lease.Path, lease.LeaseID = g.Path, g.LeaseID
	lease.Meta["leased_at"] = g.LeasedAt
	if branchExists || git.CurrentBranch(ctx, g.Path) != req.Branch {
		if _, err := git.Git(ctx, g.Path, "switch", req.Branch); err != nil {
			_ = p.Release(ctx, lease, workspace.ReleaseOpts{Force: true})
			return lease, err
		}
	}
	lease.BaseSHA = git.Tip(ctx, req.Repo, req.Base)
	return lease, nil
}

// lastJSON returns the last line that looks like a JSON object.
func lastJSON(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); bytes.HasPrefix(l, []byte("{")) {
			return l
		}
	}
	return bytes.TrimSpace(out)
}

type statusEntry struct {
	Path        string `json:"path"`
	LeaseID     string `json:"lease_id"`
	LeaseHolder string `json:"lease_holder"`
	Branch      string `json:"branch"`
}

func (p *Provider) status(ctx context.Context, repo string) ([]statusEntry, error) {
	out, err := p.run(ctx, repo, nil, "status", "--json")
	if err != nil {
		return nil, err
	}
	var list []statusEntry
	if err := json.Unmarshal(out, &list); err == nil {
		return list, nil
	}
	var wrapped struct {
		Trees []statusEntry `json:"trees"`
	}
	if err := json.Unmarshal(out, &wrapped); err != nil {
		return nil, fmt.Errorf("treehouse status: unexpected output")
	}
	return wrapped.Trees, nil
}

// existingLease finds a lease this run already holds (crash recovery).
func (p *Provider) existingLease(ctx context.Context, req workspace.AcquireRequest) (workspace.Lease, bool) {
	list, err := p.status(ctx, req.Repo)
	if err != nil {
		return workspace.Lease{}, false
	}
	for _, e := range list {
		if e.LeaseHolder == Holder(req.RunID) && e.LeaseID != "" {
			return workspace.Lease{Provider: "treehouse", Path: e.Path, LeaseID: e.LeaseID}, true
		}
	}
	return workspace.Lease{}, false
}

// Release returns the tree, guarded by the lease id so ship never returns a
// lease it doesn't hold.
func (p *Provider) Release(ctx context.Context, l workspace.Lease, opts workspace.ReleaseOpts) error {
	if l.LeaseID == "" {
		return fmt.Errorf("treehouse: refusing to return %s without a lease id", l.Path)
	}
	args := []string{"return", l.Path, "--if-lease-id", l.LeaseID}
	if opts.Force {
		args = append(args, "--force")
	}
	dir := l.Meta["repo"]
	if dir == "" {
		dir, _ = os.Getwd()
	}
	_, err := p.run(ctx, dir, nil, args...)
	return err
}

// Exists checks the path and that treehouse still lists the lease.
func (p *Provider) Exists(l workspace.Lease) bool {
	if _, err := os.Stat(l.Path); err != nil {
		return false
	}
	list, err := p.status(context.Background(), l.Meta["repo"])
	if err != nil {
		return true // can't tell; the path is there
	}
	for _, e := range list {
		if e.Path == l.Path && e.LeaseID == l.LeaseID {
			return true
		}
	}
	return false
}
