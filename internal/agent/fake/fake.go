// Package fake is a scripted agent adapter for tests and dry runs.
//
// The script maps step names to a list of entries. Visit N of a step uses
// entry N (the last entry repeats):
//
//	implement: [{outcome: done, summary: "wrote code", sleep: 1s}]
//	review:
//	  - {outcome: changes, summary: "missing tests"}
//	  - {outcome: pass, summary: "lgtm"}
//
// The script path comes from SHIP_FAKE_AGENT in the request env.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
)

// ScriptEnv names the env var holding the script path.
const ScriptEnv = brand.EnvPrefix + "FAKE_AGENT"

// PluginDirsEnv lists the request's plugin dirs (colon-separated) for a
// scripted run: snippet.
const PluginDirsEnv = brand.EnvPrefix + "FAKE_PLUGIN_DIRS"

// Entry is one scripted response.
type Entry struct {
	Outcome string            `yaml:"outcome"`
	Summary string            `yaml:"summary"`
	Vars    map[string]string `yaml:"vars"`
	Slices  []agent.SliceOut  `yaml:"slices"`
	Sleep   string            `yaml:"sleep"`
	Write   map[string]string `yaml:"write"`
	Run     string            `yaml:"run"` // shell snippet run in the workdir (e.g. git commit)
	Exit    int               `yaml:"exit"`
	Cost    float64           `yaml:"cost"`
	Tokens  int64             `yaml:"tokens"`
	// Usage breaks the tokens down; when set without tokens, tokens is
	// input + cache_write + output.
	Usage *Usage `yaml:"usage"`
	// Limited makes the first N tries hit a usage limit that resets after
	// LimitReset (default: no reset time given).
	Limited    int    `yaml:"limited"`
	LimitReset string `yaml:"limit_reset"`
	// LimitNotice reports a usage limit but still finishes, as the CLI does
	// when extra usage takes over from a spent window.
	LimitNotice bool `yaml:"limit_notice"`
	// InvalidAttempts makes the first N attempts return invalid output, to
	// exercise the correction retry.
	InvalidAttempts int `yaml:"invalid_attempts"`
	// Denials simulates permission denials.
	Denials []string `yaml:"denials"`
	// Output, when set, is returned verbatim as the structured result
	// (for callers with their own schema, like `ship pipeline refine`).
	Output map[string]any `yaml:"output"`
}

// Usage is a scripted token breakdown.
type Usage struct {
	Input      int64 `yaml:"input"`
	Output     int64 `yaml:"output"`
	CacheWrite int64 `yaml:"cache_write"`
	CacheRead  int64 `yaml:"cache_read"`
}

// Script is a parsed fake-agent script.
type Script map[string][]Entry

// LoadScript reads a script file.
func LoadScript(path string) (Script, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Script
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("fake agent script %s: %w", path, err)
	}
	return s, nil
}

// Adapter is the fake adapter.
type Adapter struct{}

// New returns the adapter.
func New() *Adapter { return &Adapter{} }

// Name implements agent.Adapter.
func (*Adapter) Name() string { return "fake" }

// Validate accepts anything.
func (*Adapter) Validate(agent.StepAgentConfig) []string { return nil }

// Check always succeeds.
func (*Adapter) Check(context.Context) (string, error) { return "fake", nil }

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return os.Getenv(key)
}

// Run plays the scripted entry for the visit.
func (a *Adapter) Run(ctx context.Context, req agent.Request, sink agent.Sink) (agent.Response, error) {
	path := envValue(req.Env, ScriptEnv)
	if path == "" {
		return agent.Response{IsError: true, ErrorText: "fake agent: no script (set " + ScriptEnv + " or use --fake-agents)", ExitCode: 1}, nil
	}
	script, err := LoadScript(path)
	if err != nil {
		return agent.Response{IsError: true, ErrorText: err.Error(), ExitCode: 1}, nil
	}
	entries := script[req.Step]
	if len(entries) == 0 {
		return agent.Response{IsError: true, ErrorText: fmt.Sprintf("fake agent: script has no entries for step %q", req.Step), ExitCode: 1}, nil
	}
	i := req.VisitNumber - 1
	if i < 0 {
		i = 0
	}
	if i >= len(entries) {
		i = len(entries) - 1
	}
	e := entries[i]
	var usage agent.Usage
	if u := e.Usage; u != nil {
		usage = agent.Usage{Input: u.Input, Output: u.Output, CacheCreation: u.CacheWrite, CacheRead: u.CacheRead}
		if e.Tokens == 0 {
			e.Tokens = usage.Tokens()
		}
	}
	session := req.SessionID
	if session == "" {
		session = req.ResumeID
	}
	transcript := func(v any) {
		if req.TranscriptW != nil {
			b, _ := json.Marshal(v)
			req.TranscriptW.Write(append(b, '\n'))
		}
	}
	transcript(map[string]any{"type": "system", "subtype": "init", "session_id": session, "model": "fake", "step": req.Step, "visit": req.VisitNumber})
	if sink != nil {
		sink.Event(agent.UIEvent{Kind: "text", Data: map[string]any{"text": fmt.Sprintf("fake agent: step %s, visit %d, entry %d", req.Step, req.VisitNumber, i+1)}})
	}

	if e.Sleep != "" {
		d, err := pipeline.ParseDuration(e.Sleep)
		if err == nil {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return agent.Response{IsError: true, ErrorText: ctx.Err().Error(), SessionID: session, ExitCode: -1}, nil
			}
		}
	}
	if req.LimitRetry < e.Limited {
		resp := agent.Response{SessionID: session, IsError: true, Limited: true, ExitCode: 1, ErrorText: "You've hit your session limit"}
		if d, err := pipeline.ParseDuration(e.LimitReset); err == nil && e.LimitReset != "" {
			resp.RetryAt = time.Now().Add(d)
		}
		return resp, nil
	}
	if e.Tokens > 0 && req.MaxTokens > 0 && e.Tokens > req.MaxTokens {
		return agent.Response{SessionID: session, IsError: true, OverBudget: "tokens", Tokens: req.MaxTokens + 1, CostUSD: e.Cost, Limited: e.LimitNotice, ExitCode: 1, ErrorText: "stopped at its token budget"}, nil
	}
	if e.Cost > 0 && req.BudgetUSD > 0 && e.Cost > req.BudgetUSD {
		return agent.Response{SessionID: session, IsError: true, OverBudget: "usd", CostUSD: req.BudgetUSD, Limited: e.LimitNotice, ExitCode: 1, ErrorText: "reached its spending limit"}, nil
	}
	if req.Attempt == 0 {
		for p, content := range e.Write {
			full := filepath.Join(req.Workdir, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return agent.Response{IsError: true, ErrorText: err.Error(), ExitCode: 1}, nil
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				return agent.Response{IsError: true, ErrorText: err.Error(), ExitCode: 1}, nil
			}
			if sink != nil {
				sink.Event(agent.UIEvent{Kind: "tool_use", Data: map[string]any{"name": "Write", "input": map[string]any{"file_path": p}}})
			}
		}
		if e.Run != "" {
			// The snippet can see which plugins a real agent would load.
			env := append(append([]string{}, req.Env...), PluginDirsEnv+"="+strings.Join(req.PluginDirs, ":"))
			if err := runShell(ctx, req.Workdir, env, e.Run); err != nil {
				return agent.Response{IsError: true, ErrorText: "fake run: " + err.Error(), SessionID: session, ExitCode: 1}, nil
			}
		}
	}
	resp := agent.Response{SessionID: session, CostUSD: e.Cost, Tokens: e.Tokens, TokenUsage: usage, ExitCode: e.Exit, Limited: e.LimitNotice}
	for _, d := range e.Denials {
		b, _ := json.Marshal(map[string]any{"tool_name": d})
		resp.PermissionDenials = append(resp.PermissionDenials, b)
	}
	if e.Exit != 0 {
		resp.IsError = true
		resp.ErrorText = fmt.Sprintf("fake agent exited with %d", e.Exit)
		transcript(map[string]any{"type": "result", "subtype": "error", "is_error": true, "session_id": session})
		return resp, nil
	}
	if req.Attempt < e.InvalidAttempts {
		resp.Structured = json.RawMessage(`{"oops": true}`)
		resp.Text = "I forgot the format."
	} else if e.Output != nil {
		b, err := json.Marshal(e.Output)
		if err != nil {
			return agent.Response{IsError: true, ErrorText: "fake output: " + err.Error(), ExitCode: 1}, nil
		}
		resp.Structured = b
		resp.Text = string(b)
	} else {
		out := agent.Output{Outcome: e.Outcome, Summary: e.Summary, Vars: e.Vars, Slices: e.Slices}
		b, _ := json.Marshal(out)
		resp.Structured = b
		resp.Text = e.Summary
	}
	if sink != nil {
		sink.Event(agent.UIEvent{Kind: "text", Data: map[string]any{"text": resp.Text}})
	}
	transcript(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session,
		"total_cost_usd": e.Cost, "structured_output": json.RawMessage(resp.Structured)})
	return resp, nil
}
