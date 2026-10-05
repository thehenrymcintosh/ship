// Package claude is the Claude Code adapter.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/proc"
)

// Valid values for Validate.
var (
	Efforts         = []string{"low", "medium", "high", "xhigh", "max"}
	PermissionModes = []string{"default", "acceptEdits", "plan", "bypassPermissions"}
)

// MaxArgPrompt is the largest prompt passed as an argument; larger prompts
// go on stdin.
const MaxArgPrompt = 100 * 1024

// Adapter runs `claude -p`.
type Adapter struct {
	// Binary is the CLI path (default "claude", resolved on PATH).
	Binary string
}

// New returns the adapter.
func New() *Adapter { return &Adapter{Binary: "claude"} }

// Name implements agent.Adapter.
func (*Adapter) Name() string { return "claude" }

// Validate checks effort and permission mode.
func (*Adapter) Validate(cfg agent.StepAgentConfig) []string {
	var out []string
	if cfg.Effort != "" && !contains(Efforts, cfg.Effort) {
		out = append(out, fmt.Sprintf("claude: invalid effort %q (want one of %s)", cfg.Effort, strings.Join(Efforts, ", ")))
	}
	if cfg.PermissionMode != "" && !contains(PermissionModes, cfg.PermissionMode) {
		out = append(out, fmt.Sprintf("claude: invalid permission_mode %q (want one of %s)", cfg.PermissionMode, strings.Join(PermissionModes, ", ")))
	}
	return out
}

// Check runs `claude --version`.
func (a *Adapter) Check(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, a.bin(), "--version").Output()
	if err != nil {
		return "", fmt.Errorf("claude CLI not usable (%s --version): %w", a.bin(), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (a *Adapter) bin() string {
	if a.Binary == "" {
		return "claude"
	}
	return a.Binary
}

// Args builds the command line (without the binary). The bool reports
// whether the prompt goes on stdin.
func Args(req agent.Request) ([]string, bool) {
	args := []string{"-p"}
	stdin := len(req.Prompt) > MaxArgPrompt
	if stdin {
		args = append(args, "--input-format", "text")
	} else {
		args = append(args, req.Prompt)
	}
	args = append(args, "--output-format", "stream-json", "--verbose")
	if len(req.OutputSchema) > 0 {
		args = append(args, "--json-schema", string(req.OutputSchema))
	}
	if req.SystemAppend != "" {
		args = append(args, "--append-system-prompt", req.SystemAppend)
	}
	if req.ResumeID != "" {
		args = append(args, "--resume", req.ResumeID)
	} else if req.SessionID != "" {
		args = append(args, "--session-id", req.SessionID)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if req.Permission.Mode != "" {
		args = append(args, "--permission-mode", req.Permission.Mode)
	}
	if len(req.Permission.Allowed) > 0 {
		args = append(args, append([]string{"--allowed-tools"}, req.Permission.Allowed...)...)
	}
	if len(req.Permission.Disallowed) > 0 {
		args = append(args, append([]string{"--disallowed-tools"}, req.Permission.Disallowed...)...)
	}
	for _, d := range req.ReadDirs {
		args = append(args, "--add-dir", d)
	}
	if req.BudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(req.BudgetUSD, 'f', 4, 64))
	}
	args = append(args, req.ExtraArgs...)
	return args, stdin
}

// Run runs one invocation and parses its stream.
func (a *Adapter) Run(ctx context.Context, req agent.Request, sink agent.Sink) (agent.Response, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	args, useStdin := Args(req)
	p := NewParser(sink)
	// Stop the agent once it has used its token budget (claude enforces the
	// dollar one itself).
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	overTokens := false
	if req.MaxTokens > 0 {
		p.onTokens = func(n int64) {
			if n > req.MaxTokens && !overTokens {
				overTokens = true
				stop()
			}
		}
	}
	lw := &lineWriter{fn: func(line []byte) {
		if req.TranscriptW != nil {
			req.TranscriptW.Write(append(append([]byte(nil), line...), '\n'))
		}
		p.Line(line)
	}}
	spec := proc.Spec{Path: a.bin(), Args: args, Dir: req.Workdir, Env: req.Env, Stdout: lw, Stderr: req.StderrW}
	if useStdin {
		spec.Stdin = strings.NewReader(req.Prompt)
	}
	if spec.Stderr == nil {
		spec.Stderr = io.Discard
	}
	res, err := proc.Run(ctx, spec)
	lw.Flush()
	if overTokens {
		resp := p.Response()
		resp.IsError, resp.OverBudget, resp.ExitCode = true, "tokens", res.ExitCode
		resp.ErrorText = fmt.Sprintf("stopped at its token budget: %d of %d tokens", resp.Tokens, req.MaxTokens)
		if resp.SessionID == "" {
			resp.SessionID = firstNonEmpty(req.ResumeID, req.SessionID)
		}
		return resp, nil
	}
	if err != nil {
		return agent.Response{IsError: true, ErrorText: err.Error(), ExitCode: -1}, err
	}
	resp := p.Response()
	resp.ExitCode = res.ExitCode
	if resp.SessionID == "" {
		resp.SessionID = firstNonEmpty(req.ResumeID, req.SessionID)
	}
	if res.ExitCode != 0 {
		resp.IsError = true
		if resp.ErrorText == "" {
			resp.ErrorText = fmt.Sprintf("claude exited with code %d", res.ExitCode)
		}
		if ctx.Err() == context.DeadlineExceeded && req.Timeout > 0 {
			resp.ErrorText = "timed out after " + req.Timeout.String()
		} else if ctx.Err() != nil {
			resp.ErrorText = "cancelled"
		}
	}
	return resp, nil
}

// Parser turns stream-json lines into UI events and a Response.
type Parser struct {
	sink      agent.Sink
	resp      agent.Response
	gotResult bool
	lastText  string
	// Tokens per API message so far (a message's usage repeats on each of
	// its content lines).
	msgTokens map[string]int64
	onTokens  func(total int64)
}

// NewParser returns a parser emitting to sink (may be nil).
func NewParser(sink agent.Sink) *Parser { return &Parser{sink: sink} }

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// system init
	SessionID      string `json:"session_id"`
	Model          string `json:"model"`
	PermissionMode string `json:"permissionMode"`
	// assistant / user
	Message *struct {
		ID      string       `json:"id"`
		Content []block      `json:"content"`
		Usage   *agent.Usage `json:"usage"`
	} `json:"message"`
	Error string `json:"error"` // assistant: an API error such as rate_limit
	// rate_limit_event
	RateLimit *struct {
		Status         string  `json:"status"`
		ResetsAt       int64   `json:"resetsAt"`
		Type           string  `json:"rateLimitType"`
		Utilization    float64 `json:"utilization"`
		UnifiedWindows map[string]struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resetsAt"`
		} `json:"unifiedWindows"`
	} `json:"rate_limit_info"`
	// result
	StructuredOutput  json.RawMessage   `json:"structured_output"`
	Result            json.RawMessage   `json:"result"`
	TotalCostUSD      float64           `json:"total_cost_usd"`
	Usage             json.RawMessage   `json:"usage"`
	IsError           bool              `json:"is_error"`
	NumTurns          int               `json:"num_turns"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
	TerminalReason    string            `json:"terminal_reason"`
	Errors            []string          `json:"errors"`
}

const toolResultLimit = 2000

func (p *Parser) emit(kind string, data any) {
	if p.sink != nil {
		p.sink.Event(agent.UIEvent{Kind: kind, Data: data})
	}
}

// Line handles one stream line. Unknown or malformed lines are ignored (they
// stay in the transcript).
func (p *Parser) Line(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var l streamLine
	if err := json.Unmarshal(line, &l); err != nil {
		return
	}
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			p.resp.SessionID = l.SessionID
			p.emit("system", map[string]any{"session_id": l.SessionID, "model": l.Model, "permission_mode": l.PermissionMode})
		}
	case "rate_limit_event":
		rl := l.RateLimit
		if rl == nil {
			return
		}
		if rl.Status == "rejected" {
			p.resp.Limited = true
			if rl.ResetsAt > 0 {
				p.resp.RetryAt = time.Unix(rl.ResetsAt, 0)
			}
		}
		rep := agent.UsageReport{Rejected: rl.Status == "rejected"}
		for name, w := range rl.UnifiedWindows {
			rep.Windows = append(rep.Windows, agent.LimitWindow{Name: name, Utilization: w.Utilization, ResetsAt: time.Unix(w.ResetsAt, 0)})
		}
		if len(rep.Windows) == 0 && rl.Type != "" && rl.ResetsAt > 0 {
			rep.Windows = append(rep.Windows, agent.LimitWindow{Name: rl.Type, Utilization: rl.Utilization, ResetsAt: time.Unix(rl.ResetsAt, 0)})
		}
		if len(rep.Windows) > 0 && p.sink != nil {
			p.sink.Event(agent.UIEvent{Kind: "usage", Data: rep})
		}
	case "assistant":
		if limitError(l.Error) {
			p.resp.Limited = true
		}
		if l.Message == nil {
			return
		}
		if u := l.Message.Usage; u != nil && l.Message.ID != "" {
			if p.msgTokens == nil {
				p.msgTokens = map[string]int64{}
			}
			p.msgTokens[l.Message.ID] = u.Tokens()
			var total int64
			for _, n := range p.msgTokens {
				total += n
			}
			p.resp.Tokens = total
			if p.onTokens != nil {
				p.onTokens(total)
			}
		}
		for _, b := range l.Message.Content {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					p.lastText = b.Text
					p.emit("text", map[string]any{"text": b.Text})
				}
			case "tool_use":
				if b.Name == "StructuredOutput" && len(b.Input) > 0 {
					p.resp.Structured = b.Input
				}
				p.emit("tool_use", map[string]any{"id": b.ID, "name": b.Name, "input": b.Input})
			}
		}
	case "user":
		if l.Message == nil {
			return
		}
		for _, b := range l.Message.Content {
			if b.Type == "tool_result" {
				p.emit("tool_result", map[string]any{"tool_use_id": b.ToolUseID, "content": truncate(contentText(b.Content), toolResultLimit), "is_error": b.IsError})
			}
		}
	case "result":
		p.gotResult = true
		if l.SessionID != "" {
			p.resp.SessionID = l.SessionID
		}
		if len(l.StructuredOutput) > 0 && string(l.StructuredOutput) != "null" {
			p.resp.Structured = l.StructuredOutput
		}
		var text string
		if len(l.Result) > 0 {
			if err := json.Unmarshal(l.Result, &text); err != nil {
				text = string(l.Result)
			}
		}
		p.resp.Text = text
		if len(p.resp.Structured) == 0 && strings.HasPrefix(strings.TrimSpace(text), "{") && json.Valid([]byte(strings.TrimSpace(text))) {
			p.resp.Structured = json.RawMessage(strings.TrimSpace(text))
		}
		p.resp.CostUSD = l.TotalCostUSD
		p.resp.Usage = l.Usage
		var u agent.Usage
		if json.Unmarshal(l.Usage, &u) == nil && u.Tokens() > 0 {
			p.resp.Tokens = u.Tokens()
		}
		if strings.Contains(l.Subtype, "budget") {
			p.resp.OverBudget = "usd"
		}
		p.resp.PermissionDenials = l.PermissionDenials
		if l.IsError || (l.Subtype != "" && l.Subtype != "success") {
			p.resp.IsError = true
			msg := text
			if msg == "" && len(l.Errors) > 0 {
				msg = strings.Join(l.Errors, "; ")
			}
			if msg == "" {
				msg = "claude reported " + firstNonEmpty(l.Subtype, "an error")
			}
			p.resp.ErrorText = msg
			if limitText(msg) {
				p.resp.Limited = true
			}
		}
	}
}

// limitError reports whether an assistant message's API error is one to
// wait out rather than fail on.
func limitError(e string) bool {
	switch e {
	case "rate_limit", "overloaded", "overloaded_error", "rate_limit_error":
		return true
	}
	return false
}

// limitText catches limits reported only as text.
func limitText(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{"usage limit", "session limit", "rate limit", "api error: 429", "api error: 529", "overloaded"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// Response returns the accumulated response.
func (p *Parser) Response() agent.Response {
	r := p.resp
	if !p.gotResult {
		r.IsError = true
		if r.ErrorText == "" {
			r.ErrorText = "claude produced no result"
		}
		if r.Text == "" {
			r.Text = p.lastText
		}
	}
	return r
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func contains(s []string, x string) bool {
	for _, y := range s {
		if y == x {
			return true
		}
	}
	return false
}

// lineWriter splits writes into lines.
type lineWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	fn  func([]byte)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		b := w.buf.Bytes()
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := append([]byte(nil), b[:i]...)
		w.buf.Next(i + 1)
		w.fn(line)
	}
}

func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.fn(append([]byte(nil), w.buf.Bytes()...))
		w.buf.Reset()
	}
}
