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
	EvPauseRequested    = "pause_requested"
	EvResumed           = "resumed"
	EvVersioned         = "pipeline_versioned"
	EvPRPolled          = "pr_polled"
	EvUpgraded          = "pipeline_upgraded"
	EvBudgetRaised      = "budget_raised"
	EvAfterReleased     = "after_released"
	EvCostAdded         = "cost_added"
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
	// After is the run this one waits for: it starts once that run is done.
	// AfterStack branches it from that run's branch instead of its base.
	After      string `json:"after,omitempty"`
	AfterStack bool   `json:"after_stack,omitempty"`
	// The pipeline version this run uses, and where its history lives.
	PipelineVersion int    `json:"pipeline_version,omitempty"`
	PipelineHash    string `json:"pipeline_hash,omitempty"`
	HistoryDir      string `json:"history_dir,omitempty"`
	// PipelineFolders maps each folder pipeline of the run's closure to its
	// folder: repo-relative when it's in the repo (the run uses the copy in
	// its worktree), else absolute.
	PipelineFolders map[string]string `json:"pipeline_folders,omitempty"`
	// Warnings about the run's setup, e.g. files its pipeline uses that its
	// worktree won't have.
	Warnings []string `json:"warnings,omitempty"`
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
	Thread      string `json:"thread,omitempty"`   // the agent conversation this visit belongs to
	HeadSHA     string `json:"head_sha,omitempty"` // worktree HEAD when the visit started
}

// SeqOnly is the data of visit_dequeued, visit_interrupted, slices_approved.
type SeqOnly struct {
	Seq int `json:"seq"`
}

// CostAdded is spending outside any visit's own agent, such as summarising
// a check-in; it counts toward the run's cost.
type CostAdded struct {
	Seq     int     `json:"seq"` // the visit it was for
	What    string  `json:"what"`
	CostUSD float64 `json:"cost_usd"`
	Tokens  int64   `json:"tokens,omitempty"`
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
	Tokens            int64             `json:"tokens,omitempty"`
	Usage             *TokenUsage       `json:"usage,omitempty"` // breakdown of an agent visit's tokens
	DurationMS        int64             `json:"duration_ms"`
	SessionID         string            `json:"session_id,omitempty"`
	PermissionDenials int               `json:"permission_denials,omitempty"`
}

// TokenUsage breaks an agent visit's tokens down, summed over its tries.
// Tokens (the total budgets count) is Input + CacheWrite + Output.
type TokenUsage struct {
	Input      int64 `json:"input,omitempty"`
	Output     int64 `json:"output,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
	CacheRead  int64 `json:"cache_read,omitempty"`
}

// Add adds o to u.
func (u *TokenUsage) Add(o TokenUsage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheWrite += o.CacheWrite
	u.CacheRead += o.CacheRead
}

// IsZero reports whether nothing was counted.
func (u TokenUsage) IsZero() bool { return u == TokenUsage{} }

// Total is the count budgets use: everything but cache reads.
func (u TokenUsage) Total() int64 { return u.Input + u.CacheWrite + u.Output }

// Ptr returns &u, or nil when u is zero (for omitempty fields).
func (u TokenUsage) Ptr() *TokenUsage {
	if u.IsZero() {
		return nil
	}
	return &u
}

// BudgetRaised adds to a run's budget beyond its pipeline's limits.
type BudgetRaised struct {
	USD    float64 `json:"usd,omitempty"`
	Tokens int64   `json:"tokens,omitempty"`
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
	AskKindAfter       = "after" // the run waited on ended without done
)

// Answers to an AskKindAfter question.
const (
	AfterStartAnyway = "start anyway"
	AfterCancel      = "cancel"
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

// Note scopes: who a check-in note is for.
const (
	NoteForStep = "step" // the next step only, as the check-in's handover (the default)
	NoteForRun  = "run"  // every later agent step of the run, and its slices
)

// AskAnswered closes a question.
type AskAnswered struct {
	Seq    int    `json:"seq"`
	Choice string `json:"choice"`
	Note   string `json:"note"`
	For    string `json:"for,omitempty"` // NoteForRun, or "" for the next step
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

// PauseChange records a pause request or a resume.
type PauseChange struct {
	All    bool   `json:"all,omitempty"` // also children
	Source string `json:"source,omitempty"`
}

// Versioned records the pipeline version a run uses, computed once its
// worktree exists (so it reflects what the run's agents actually see).
type Versioned struct {
	Version int    `json:"version"`
	Hash    string `json:"hash"`
}

// Upgraded records a run moving onto the pipeline as it is now. The run is
// versioned again (a pipeline_versioned event follows).
type Upgraded struct {
	FromVersion int      `json:"from_version,omitempty"`
	FromHash    string   `json:"from_hash,omitempty"`
	Step        string   `json:"step"`
	Warnings    []string `json:"warnings,omitempty"` // replace the run's setup warnings
	// The new closure's pipeline folders (see RunCreated).
	PipelineFolders map[string]string `json:"pipeline_folders,omitempty"`
}

// PRStatus is the latest observation of the PR a pr step watches.
type PRStatus struct {
	Step            string     `json:"step"`
	Seq             int        `json:"seq"`
	URL             string     `json:"url,omitempty"`
	Number          int        `json:"number,omitempty"`
	State           string     `json:"state,omitempty"` // OPEN, CLOSED, MERGED; "" when there's no PR yet
	HeadSHA         string     `json:"head_sha,omitempty"`
	ReviewDecision  string     `json:"review_decision,omitempty"`
	ChecksPass      int        `json:"checks_pass"`
	ChecksFail      int        `json:"checks_fail"`
	ChecksPending   int        `json:"checks_pending"`
	Failing         []string   `json:"failing,omitempty"`
	PendingComments int        `json:"pending_comments"` // new review feedback not yet sent on
	LastCommentAt   *time.Time `json:"last_comment_at,omitempty"`
	Trigger         string     `json:"trigger"`
	SettleSeconds   int        `json:"settle_seconds"`
	Note            string     `json:"note,omitempty"` // e.g. "no PR for branch x yet"
}

// AfterReleased ends a run's wait for the run it starts after. How is
// done (that run finished done), start_now or start anyway (a person
// chose to); Base, when set, is the base it now branches from.
type AfterReleased struct {
	How  string `json:"how"`
	Base string `json:"base,omitempty"`
}

// RunFinished ends the run.
type RunFinished struct {
	Status Status `json:"status"`
	Reason string `json:"reason,omitempty"`
}
