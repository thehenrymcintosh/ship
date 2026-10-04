// Package store persists runs: the append-only event log (the authoritative
// record), the snapshot cache derived from it, the run dir layout and locks.
package store

import (
	"encoding/json"
	"time"

	"github.com/thehenrymcintosh/ship/internal/workspace"
)

// Event is one line of events.jsonl.
type Event struct {
	Seq   int             `json:"seq"`
	TS    time.Time       `json:"ts"`
	Type  string          `json:"type"`
	Actor string          `json:"actor"`
	Data  json.RawMessage `json:"data"`
}

// Event types.
const (
	EvRunCreated        = "run_created"
	EvWorkspaceAcquired = "workspace_acquired"
	EvWorkspaceReleased = "workspace_released"
	EvWorkspaceMissing  = "workspace_missing"
	EvVisitStarted      = "visit_started"
	EvVisitDequeued     = "visit_dequeued"
	EvVisitFinished     = "visit_finished"
	EvVisitInterrupted  = "visit_interrupted"
	EvTransition        = "transition"
	EvVarSet            = "var_set"
	EvAskPending        = "ask_pending"
	EvAskAnswered       = "ask_answered"
	EvWaitPolled        = "wait_polled"
	EvSlicesProposed    = "slices_proposed"
	EvSlicesApproved    = "slices_approved"
	EvChildStarted      = "child_started"
	EvChildFinished     = "child_finished"
	EvBaseMoved         = "base_moved"
	EvStatusChanged     = "status_changed"
	EvCommand           = "command"
	EvRunFinished       = "run_finished"
)

// Actors.
const (
	ActorEngine = "engine"
	ActorUser   = "user"
)

// Transition reasons.
const (
	ReasonNormal    = "normal"
	ReasonExhausted = "exhausted"
	ReasonError     = "error"
	ReasonManual    = "manual"
	ReasonHuman     = "human"
	ReasonBudget    = "budget"
)

// RunCreated starts every log.
type RunCreated struct {
	ID          string            `json:"id"`
	Pipeline    string            `json:"pipeline"`
	Repo        string            `json:"repo"`
	RepoOrigin  string            `json:"repo_origin,omitempty"`
	BriefTitle  string            `json:"brief_title"`
	Start       string            `json:"start"`
	Vars        map[string]string `json:"vars"`
	Parent      *ParentRef        `json:"parent,omitempty"`
	Slice       *SliceRef         `json:"slice,omitempty"`
	ShipVersion string            `json:"ship_version"`
	Provider    string            `json:"provider"`
	Branch      string            `json:"branch"`
	Base        string            `json:"base,omitempty"`
	FakeAgents  string            `json:"fake_agents,omitempty"`
}

// WorkspaceAcquired records a lease.
type WorkspaceAcquired struct {
	Lease workspace.Lease `json:"lease"`
}

// WorkspaceReleased records a release.
type WorkspaceReleased struct {
	Forced bool `json:"forced"`
}

// WorkspaceMissing flags a lease whose path vanished.
type WorkspaceMissing struct {
	Path string `json:"path"`
}

// VisitStarted opens a visit.
type VisitStarted struct {
	Seq         int    `json:"seq"`
	Step        string `json:"step"`
	Type        string `json:"type"`
	VisitNumber int    `json:"visit_number"`
	CameFrom    string `json:"came_from"`
	SessionID   string `json:"session_id,omitempty"`
	ResumeID    string `json:"resume_id,omitempty"`
	Queued      bool   `json:"queued"`
	Dir         string `json:"dir"`
}

// SeqOnly is the data of visit_dequeued, visit_interrupted, slices_approved.
type SeqOnly struct {
	Seq int `json:"seq"`
}

// StepError says why a visit produced `error`.
type StepError struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// VisitFinished closes a visit.
type VisitFinished struct {
	Seq               int               `json:"seq"`
	Outcome           string            `json:"outcome"`
	Summary           string            `json:"summary"`
	Vars              map[string]string `json:"vars,omitempty"`
	Error             *StepError        `json:"error,omitempty"`
	CostUSD           float64           `json:"cost_usd"`
	DurationMS        int64             `json:"duration_ms"`
	SessionID         string            `json:"session_id,omitempty"`
	PermissionDenials int               `json:"permission_denials,omitempty"`
}

// Transition moves the run to another step.
type Transition struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	// Reset zeroes the target's visit counter (human/manual transitions).
	Reset bool `json:"reset,omitempty"`
}

// VarSet sets a variable.
type VarSet struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	ByStep string `json:"by_step,omitempty"`
}

// Ask kinds.
const (
	AskKindAsk         = "ask"
	AskKindSplitReview = "split_review"
	AskKindVar         = "var"
)

// AskPending opens a question for a human.
type AskPending struct {
	Seq      int      `json:"seq"`
	Kind     string   `json:"kind"`
	Question string   `json:"question"`
	Choices  []string `json:"choices"`
	Input    string   `json:"input"`
	Show     []string `json:"show,omitempty"`
	Var      string   `json:"var,omitempty"`
}

// AskAnswered closes a question.
type AskAnswered struct {
	Seq    int    `json:"seq"`
	Choice string `json:"choice"`
	Note   string `json:"note"`
}

// WaitPolled records one poll.
type WaitPolled struct {
	Seq      int    `json:"seq"`
	N        int    `json:"n"`
	Exit     int    `json:"exit"`
	LastLine string `json:"last_line"`
}

// Slice is one approved or proposed slice.
type Slice struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	File   string `json:"file"`
	Number int    `json:"number"`
}

// SlicesProposed records a split result.
type SlicesProposed struct {
	Seq    int     `json:"seq"`
	Slices []Slice `json:"slices"`
}

// ChildStarted links a child run.
type ChildStarted struct {
	ChildID  string `json:"child_id"`
	SliceKey string `json:"slice_key"`
	Number   int    `json:"number"`
}

// ChildFinished records a child's terminal status.
type ChildFinished struct {
	ChildID string `json:"child_id"`
	Status  Status `json:"status"`
}

// BaseMoved flags base drift.
type BaseMoved struct {
	OldSHA string `json:"old_sha"`
	NewSHA string `json:"new_sha"`
}

// StatusChanged changes the run status.
type StatusChanged struct {
	From   Status `json:"from"`
	To     Status `json:"to"`
	Reason string `json:"reason,omitempty"`
}

// Command records a manual control.
type Command struct {
	Name   string            `json:"name"`
	Args   map[string]string `json:"args,omitempty"`
	Actor  string            `json:"actor"`
	Source string            `json:"source"`
}

// RunFinished ends the run.
type RunFinished struct {
	Status Status `json:"status"`
	Reason string `json:"reason,omitempty"`
}
