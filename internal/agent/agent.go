// Package agent defines agent adapters: the interface every agent CLI
// implements, the registry, the preamble and the structured-output schemas.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/merlin-digital/ship/internal/pipeline"
)

// StepAgentConfig is the effective agent configuration of a step.
type StepAgentConfig = pipeline.AgentConfig

// Adapter runs one agent CLI.
type Adapter interface {
	Name() string
	// Validate checks effort/permission/model values (E014). It returns
	// human-readable messages.
	Validate(cfg StepAgentConfig) []string
	// Check verifies the CLI is installed and usable, returning its version.
	Check(ctx context.Context) (string, error)
	Run(ctx context.Context, req Request, sink Sink) (Response, error)
}

// Permission is the agent's permission setup.
type Permission struct {
	Mode       string
	Allowed    []string
	Disallowed []string
}

// Request is one agent invocation.
type Request struct {
	Workdir      string
	Prompt       string // rendered: "/skill args\n\n<prompt>" or plain prompt
	SystemAppend string // preamble
	Model        string
	Effort       string
	Permission   Permission
	ReadDirs     []string        // run dir, so brief/handovers outside the worktree are readable
	OutputSchema json.RawMessage // structured-output schema
	SessionID    string          // new session id to assign (fresh)
	ResumeID     string          // session to resume
	BudgetUSD    float64         // remaining run budget, 0 = none
	Timeout      time.Duration
	Env          []string
	ExtraArgs    []string
	TranscriptW  io.Writer // raw stream output
	StderrW      io.Writer

	// Identify the visit, for adapters (like fake) that script by step.
	RunID       string
	Step        string
	VisitNumber int
	Attempt     int // 0 = first try, 1 = correction retry
}

// Response is what an invocation produced.
type Response struct {
	Structured        json.RawMessage
	Text              string // final text result, for diagnostics
	SessionID         string
	CostUSD           float64
	Usage             json.RawMessage
	IsError           bool
	ErrorText         string
	PermissionDenials []json.RawMessage
	ExitCode          int
}

// UIEvent is one live event for the UI: assistant text, a tool call or a
// tool result.
type UIEvent struct {
	Kind string `json:"kind"` // text | tool_use | tool_result | system
	Data any    `json:"data"`
}

// Sink receives live events.
type Sink interface{ Event(e UIEvent) }

// SinkFunc adapts a function to Sink.
type SinkFunc func(UIEvent)

// Event implements Sink.
func (f SinkFunc) Event(e UIEvent) { f(e) }

// Registry holds adapters by name.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Adapter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[string]Adapter{}} }

// Register adds a.
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[a.Name()] = a
}

// Get returns the adapter named cli.
func (r *Registry) Get(cli string) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.m[cli]
	if !ok {
		return nil, fmt.Errorf("unknown agent cli %q (available: %v)", cli, r.namesLocked())
	}
	return a, nil
}

// Names lists registered adapters.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.namesLocked()
}

func (r *Registry) namesLocked() []string {
	var out []string
	for k := range r.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Check validates a config for E014: the cli must exist and accept the
// effort and permission mode.
func (r *Registry) Check(cfg StepAgentConfig) []string {
	cli := cfg.CLI
	if cli == "" {
		cli = "claude"
	}
	a, err := r.Get(cli)
	if err != nil {
		return []string{err.Error()}
	}
	return a.Validate(cfg)
}
