// Package steps holds the step executors: run, ask, wait, agent and
// split. Fanout lives in the engine because it creates and supervises runs.
package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
	"github.com/thehenrymcintosh/ship/internal/tmpl"
)

// Executor runs one visit. The error is for engine bugs, not step failures:
// step failures are Results with Outcome "error".
type Executor interface {
	Execute(ctx context.Context, v *Visit) (Result, error)
}

// Visit is everything an executor needs for one visit.
type Visit struct {
	RunID        string
	RunDir       string
	Worktree     string
	Repo         string
	PipelineName string
	Pipeline     *pipeline.Pipeline
	StepName     string
	Step         *pipeline.Step
	Seq          int
	Number       int    // visit.number
	Dir          string // absolute visit dir
	CameFrom     string
	Scope        *tmpl.Scope
	Env          []string
	Timeout      time.Duration
	OutputTail   int
	Prev         *agent.PrevInfo
	Snapshot     *store.RunSnapshot
	BriefPath    string
	Acceptance   string
	// RunNotes are check-in notes for the rest of the run (and its
	// parent's, for a slice), oldest first.
	RunNotes []agent.RunNote

	// Agent sessions.
	SessionID  string // fresh session to assign
	ResumeID   string // session to resume
	ResumeKind string // "", "continue", "shared", "interrupted", "resplit"
	Note       string // re-split note
	Thread     string // the named conversation, if any
	Since      string // for a resumed conversation: what happened since its last turn
	// ForceCLI overrides every agent step's cli (fake-agent dry runs).
	ForceCLI string
	// PluginDirs are Claude Code plugins to load: the pipeline's folder,
	// for a folder pipeline (its skills).
	PluginDirs []string

	// Resumed is set when recovery re-enters an unfinished visit (asks,
	// split reviews, waits): the executor must not repeat what the log
	// already records.
	Resumed   bool
	StartedAt time.Time

	RT Runtime
}

// File returns a path inside the visit dir.
func (v *Visit) File(name string) string { return filepath.Join(v.Dir, name) }

// Result is how a visit ended.
type Result struct {
	Outcome           string
	Summary           string
	Vars              map[string]string
	Error             *store.StepError
	Cost              float64
	Tokens            int64
	TokenUsage        store.TokenUsage
	ExitCode          *int
	SessionID         string
	PermissionDenials []json.RawMessage
	Usage             json.RawMessage
	Polls             int
	Output            []string // tail lines for the handover (run/wait)
	// HumanReset marks a result chosen by a human (ask choice, split
	// approval): the transition resets the target's counter.
	HumanReset bool
	// Internal is set for engine-level routing: "resplit" or "stop"
	// (split review actions).
	Internal string
	Extra    json.RawMessage
}

// Outcomes produced by the engine itself.
const (
	OutcomeError     = "error"
	OutcomeCancelled = "cancelled"
)

// ErrorResult builds an `error` result.
func ErrorResult(reason, msg string) Result {
	return Result{Outcome: OutcomeError, Summary: msg, Error: &store.StepError{Reason: reason, Message: msg}}
}

// Command is a human action routed to an awaiting executor.
type Command struct {
	Name   string // "answer" or "split_review"
	Choice string
	Note   string
	For    string // who the note is for: store.NoteForRun, or "" for the next step
	Action string // split review: approve | resplit | reload | stop
	Reply  chan error
}

// Respond sends err to the command's caller, if it waits.
func (c Command) Respond(err error) {
	if c.Reply != nil {
		select {
		case c.Reply <- err:
		default:
		}
	}
}

// Runtime is the engine side of a visit.
type Runtime interface {
	Emit(typ string, data any) error
	SetStatus(st store.Status, reason string) error
	// Output publishes a live chunk (stream: stdout | stderr | agent).
	Output(stream string, chunk []byte)
	AgentEvent(ev agent.UIEvent)
	// Await blocks until a human command arrives for this visit.
	Await(ctx context.Context) (Command, error)
	Agents() *agent.Registry
	AgentBase() pipeline.AgentConfig
	Notify(title, message string)
	Snapshot() *store.RunSnapshot
	// Idle tells the engine the visit's agent work is over and it now only
	// waits for a human: its agent slot is released and it no longer counts
	// as executing (for restarts).
	Idle()
}

// InvalidError rejects a human command (bad choice, missing note…).
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

// ErrNotAwaiting is returned when a command arrives with nobody to take it.
var ErrNotAwaiting = errors.New("the run isn't waiting for that")

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func openLog(path string, appendMode bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o600)
}

func intPtr(i int) *int { return &i }
