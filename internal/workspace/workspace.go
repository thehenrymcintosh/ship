// Package workspace defines where runs do their work: a git worktree, a
// treehouse lease, or the main checkout.
package workspace

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Provider acquires and releases workspaces.
type Provider interface {
	Name() string
	Check(ctx context.Context, repo string) error
	Acquire(ctx context.Context, req AcquireRequest) (Lease, error)
	Release(ctx context.Context, lease Lease, opts ReleaseOpts) error
	Exists(lease Lease) bool
}

// AcquireRequest asks for a workspace for one run.
type AcquireRequest struct {
	Repo   string // main checkout
	RunID  string
	Branch string
	Base   string // branch or ref
	Env    []string
}

// Lease is an acquired workspace.
type Lease struct {
	Provider string            `json:"provider"`
	Path     string            `json:"path"`
	Branch   string            `json:"branch"`
	Base     string            `json:"base"`
	BaseSHA  string            `json:"base_sha,omitempty"`
	LeaseID  string            `json:"lease_id,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}

// ReleaseOpts controls release.
type ReleaseOpts struct{ Force bool }

// Registry maps provider names to providers.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Provider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[string]Provider{}} }

// Register adds p.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[p.Name()] = p
}

// Get returns a provider by name.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("unknown workspace provider %q (have %v)", name, r.names())
	}
	return p, nil
}

func (r *Registry) names() []string {
	var out []string
	for k := range r.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
