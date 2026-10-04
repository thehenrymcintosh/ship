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

	"github.com/merlin-digital/ship/internal/agent"
	"github.com/merlin-digital/ship/internal/brand"
	"github.com/merlin-digital/ship/internal/pipeline"
)

// ScriptEnv names the env var holding the script path.
const ScriptEnv = brand.EnvPrefix + "FAKE_AGENT"

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
	// InvalidAttempts makes the first N attempts return invalid output, to
	// exercise the correction retry.
	InvalidAttempts int `yaml:"invalid_attempts"`
	// Denials simulates permission denials.
	Denials []string `yaml:"denials"`
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
			if err := runShell(ctx, req.Workdir, req.Env, e.Run); err != nil {
				return agent.Response{IsError: true, ErrorText: "fake run: " + err.Error(), SessionID: session, ExitCode: 1}, nil
			}
		}
	}
	resp := agent.Response{SessionID: session, CostUSD: e.Cost, ExitCode: e.Exit}
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
