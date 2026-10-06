package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thehenrymcintosh/ship/internal/agent"
)

func TestParserGolden(t *testing.T) {
	f, err := os.Open("../../../testdata/claude/pass.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var events []agent.UIEvent
	p := NewParser(agent.SinkFunc(func(e agent.UIEvent) { events = append(events, e) }))
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		p.Line(sc.Bytes())
	}
	r := p.Response()
	if r.IsError || r.SessionID != "ac964fb6-728c-4731-b9e4-006f7d78935b" || r.CostUSD <= 0 {
		t.Fatalf("%+v", r)
	}
	var out agent.Output
	if err := json.Unmarshal(r.Structured, &out); err != nil || out.Outcome != "pass" {
		t.Fatalf("structured %s: %v", r.Structured, err)
	}
	schema := agent.OutcomeSchema([]string{"pass", "fail"}, nil)
	if err := agent.ValidateOutput(schema, r.Structured); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, ","); got != "system,tool_use,tool_result,tool_use,tool_result" {
		t.Errorf("events %s", got)
	}
}

func TestParserFallbacks(t *testing.T) {
	p := NewParser(nil)
	p.Line([]byte(`{"type":"result","subtype":"success","result":"{\"outcome\":\"pass\",\"summary\":\"s\"}","total_cost_usd":0.1}`))
	if r := p.Response(); string(r.Structured) != `{"outcome":"pass","summary":"s"}` || r.IsError {
		t.Fatalf("%+v", r)
	}
	p = NewParser(nil)
	p.Line([]byte(`{"type":"result","subtype":"error_max_turns","is_error":true}`))
	if r := p.Response(); !r.IsError || !strings.Contains(r.ErrorText, "error_max_turns") {
		t.Fatalf("%+v", r)
	}
	p = NewParser(nil)
	p.Line([]byte(`not json`))
	if r := p.Response(); !r.IsError {
		t.Fatal("no result line should be an error")
	}
}

func TestArgs(t *testing.T) {
	args, stdin := Args(agent.Request{Prompt: "/review", ResumeID: "r", SessionID: "s", Model: "opus", Permission: agent.Permission{Mode: "acceptEdits", Allowed: []string{"Bash(make *)"}}, ReadDirs: []string{"/run"}, PluginDirs: []string{"/wt/.ship/pipelines/pr"}, BudgetUSD: 1.5, OutputSchema: json.RawMessage(`{}`)})
	got := strings.Join(args, " ")
	for _, want := range []string{"-p /review", "--resume r", "--model opus", "--permission-mode acceptEdits", "--allowed-tools Bash(make *)", "--add-dir /run", "--plugin-dir /wt/.ship/pipelines/pr", "--max-budget-usd 1.5000", "--json-schema {}"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if stdin || strings.Contains(got, "--session-id") {
		t.Error(got)
	}
	_, stdin = Args(agent.Request{Prompt: strings.Repeat("x", MaxArgPrompt+1)})
	if !stdin {
		t.Error("large prompt should use stdin")
	}
}

// TestResumedSessionCost: on --resume, claude's total_cost_usd (and the
// --max-budget-usd it enforces) cover the whole session, so the adapter
// reports only this invocation's share and offsets the budget.
func TestResumedSessionCost(t *testing.T) {
	dir := t.TempDir()
	bin := dir + "/claude"
	script := "#!/bin/sh\necho \"$@\" > " + dir + "/args\n" +
		`echo '{"type":"system","subtype":"init","session_id":"s1"}'` + "\n" +
		`echo '{"type":"result","subtype":"success","session_id":"s1","result":"ok","total_cost_usd":0.0146,"usage":{"input_tokens":10,"cache_creation_input_tokens":849,"output_tokens":5}}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Binary: bin}
	r, err := a.Run(context.Background(), agent.Request{Workdir: dir, Prompt: "again", ResumeID: "s1", SessionCostUSD: 0.0105, BudgetUSD: 1}, nil)
	if err != nil || r.IsError {
		t.Fatalf("%v %+v", err, r)
	}
	if r.CostUSD < 0.00409 || r.CostUSD > 0.00411 {
		t.Errorf("cost %v, want this invocation's 0.0041", r.CostUSD)
	}
	if r.Tokens != 864 {
		t.Errorf("tokens %d (usage is per invocation already)", r.Tokens)
	}
	args, _ := os.ReadFile(dir + "/args")
	if !strings.Contains(string(args), "--max-budget-usd 1.0105") {
		t.Errorf("budget should be offset by the session's cost: %s", args)
	}
	// Runs recorded before this fix stored running totals, so the session
	// cost can exceed what claude reports: never go negative.
	r, _ = a.Run(context.Background(), agent.Request{Workdir: dir, Prompt: "again", ResumeID: "s1", SessionCostUSD: 0.05}, nil)
	if r.CostUSD != 0 {
		t.Errorf("cost %v, want clamped to 0", r.CostUSD)
	}
	// A fresh session's total is its own.
	r, _ = a.Run(context.Background(), agent.Request{Workdir: dir, Prompt: "new", SessionID: "s1", SessionCostUSD: 0.0105}, nil)
	if r.CostUSD != 0.0146 {
		t.Errorf("fresh cost %v", r.CostUSD)
	}
}

// TestLive runs real Claude with haiku (SHIP_LIVE_CLAUDE=1).
func TestLive(t *testing.T) {
	if os.Getenv("SHIP_LIVE_CLAUDE") != "1" {
		t.Skip("set SHIP_LIVE_CLAUDE=1 to run")
	}
	dir := t.TempDir()
	os.WriteFile(dir+"/note.txt", []byte("hello\n"), 0o644)
	var transcript bytes.Buffer
	schema := agent.OutcomeSchema([]string{"pass", "fail"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r, err := New().Run(ctx, agent.Request{
		Workdir: dir, Prompt: "Read note.txt and report outcome pass with a one-line summary.",
		SystemAppend: "You are unattended.", Model: "haiku", Permission: agent.Permission{Mode: "acceptEdits"},
		OutputSchema: schema, SessionID: uuid.NewString(), Env: os.Environ(), TranscriptW: &transcript,
	}, nil)
	if err != nil || r.IsError {
		t.Fatalf("%v %+v\n%s", err, r, transcript.String())
	}
	if err := agent.ValidateOutput(schema, r.Structured); err != nil {
		t.Fatal(err)
	}
	t.Logf("cost $%.4f", r.CostUSD)
}

func TestParserLimitsAndTokens(t *testing.T) {
	p := NewParser(nil)
	var seen int64
	p.onTokens = func(n int64) { seen = n }
	for _, l := range []string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1791233400}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":5000,"output_tokens":20}}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","name":"Bash"}],"usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":5000,"output_tokens":20}}}`,
		`{"type":"assistant","message":{"id":"m2","content":[],"usage":{"input_tokens":5,"output_tokens":5}}}`,
	} {
		p.Line([]byte(l))
	}
	if r := p.Response(); r.Limited || seen != 140 || r.TokenUsage.CacheRead != 5000 || r.TokenUsage.Input != 15 {
		t.Fatalf("limited %v tokens %d", r.Limited, seen)
	}
	// The session limit as claude reports it.
	p.Line([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1791215400,"rateLimitType":"five_hour"}}`))
	p.Line([]byte(`{"type":"assistant","message":{"id":"m3","model":"<synthetic>","content":[{"type":"text","text":"You've hit your session limit · resets 4:50pm (Europe/London)"}]},"error":"rate_limit"}`))
	p.Line([]byte(`{"type":"result","subtype":"success","is_error":true,"result":"You've hit your session limit · resets 4:50pm (Europe/London)","usage":{"input_tokens":34,"cache_creation_input_tokens":54791,"cache_read_input_tokens":773291,"output_tokens":13426}}`))
	r := p.Response()
	if !r.Limited || r.RetryAt.Unix() != 1791215400 || r.Tokens != 34+54791+13426 || r.TokenUsage.CacheRead != 773291 || r.TokenUsage.Output != 13426 {
		t.Fatalf("%+v", r)
	}
}
