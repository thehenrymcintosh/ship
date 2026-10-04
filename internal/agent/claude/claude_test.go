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

	"github.com/merlin-digital/ship/internal/agent"
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
	args, stdin := Args(agent.Request{Prompt: "/review", ResumeID: "r", SessionID: "s", Model: "opus", Permission: agent.Permission{Mode: "acceptEdits", Allowed: []string{"Bash(make *)"}}, ReadDirs: []string{"/run"}, BudgetUSD: 1.5, OutputSchema: json.RawMessage(`{}`)})
	got := strings.Join(args, " ")
	for _, want := range []string{"-p /review", "--resume r", "--model opus", "--permission-mode acceptEdits", "--allowed-tools Bash(make *)", "--add-dir /run", "--max-budget-usd 1.5000", "--json-schema {}"} {
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
