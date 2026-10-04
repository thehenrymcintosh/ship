package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/thehenrymcintosh/ship/internal/workspace"
)

// Status is a run status.
type Status string

// Statuses.
const (
	StatusStarting       Status = "starting"
	StatusRunning        Status = "running"
	StatusWaiting        Status = "waiting"
	StatusAsking         Status = "asking"
	StatusFannedOut      Status = "fanned_out"
	StatusNeedsAttention Status = "needs_attention"
	StatusDone           Status = "done"
	StatusStopped        Status = "stopped"
	StatusFailed         Status = "failed"
	StatusCancelled      Status = "cancelled"
)

// Terminal reports whether the status ends the run.
func (s Status) Terminal() bool {
	switch s {
	case StatusDone, StatusStopped, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// InInbox reports whether a run with this status waits for a human.
func (s Status) InInbox() bool { return s == StatusAsking || s == StatusNeedsAttention }

// ParentRef links a child run to its parent.
type ParentRef struct {
	ID   string `json:"id"`
	Step string `json:"step"`
}

// SliceRef describes the slice a child run builds.
type SliceRef struct {
	Key    string `json:"key"`
	Number int    `json:"number"`
	Count  int    `json:"count"`
	Title  string `json:"title"`
}

// ChildRef is a denormalised view of a child run.
type ChildRef struct {
	ID       string `json:"id"`
	SliceKey string `json:"slice_key"`
	Number   int    `json:"number"`
	Status   Status `json:"status"`
}

// Ask is a pending question.
type Ask struct {
	Seq      int       `json:"seq"`
	Kind     string    `json:"kind"`
	Question string    `json:"question"`
	Choices  []string  `json:"choices"`
	Input    string    `json:"input"`
	Show     []string  `json:"show,omitempty"`
	Var      string    `json:"var,omitempty"`
	Since    time.Time `json:"since"`
}

// VisitSummary is the snapshot's view of a visit.
type VisitSummary struct {
	Seq               int        `json:"seq"`
	Step              string     `json:"step"`
	Type              string     `json:"type"`
	VisitNumber       int        `json:"visit_number"`
	CameFrom          string     `json:"came_from,omitempty"`
	Dir               string     `json:"dir"`
	Outcome           string     `json:"outcome,omitempty"`
	Summary           string     `json:"summary,omitempty"`
	Error             *StepError `json:"error,omitempty"`
	Started           time.Time  `json:"started"`
	Finished          *time.Time `json:"finished,omitempty"`
	DurationMS        int64      `json:"duration_ms,omitempty"`
	CostUSD           float64    `json:"cost_usd,omitempty"`
	SessionID         string     `json:"session_id,omitempty"`
	ResumeID          string     `json:"resume_id,omitempty"`
	Queued            bool       `json:"queued,omitempty"`
	Interrupted       bool       `json:"interrupted,omitempty"`
	Polls             int        `json:"polls,omitempty"`
	LastPoll          string     `json:"last_poll,omitempty"`
	PermissionDenials int        `json:"permission_denials,omitempty"`
}

// Running reports whether the visit hasn't finished.
func (v *VisitSummary) Running() bool { return v.Finished == nil && !v.Interrupted }

// RunSnapshot is the cached state of a run. It is derived entirely
// from events; Rebuild replays them into an identical value.
type RunSnapshot struct {
	ID                string            `json:"id"`
	Pipeline          string            `json:"pipeline"`
	Repo              string            `json:"repo"`
	RepoOrigin        string            `json:"repo_origin,omitempty"`
	Title             string            `json:"title"`
	Status            Status            `json:"status"`
	StatusReason      string            `json:"status_reason,omitempty"`
	CurrentStep       string            `json:"current_step"`
	CameFrom          string            `json:"came_from,omitempty"`
	Vars              map[string]string `json:"vars"`
	VisitCounts       map[string]int    `json:"visit_counts"`
	VisitTotals       map[string]int    `json:"visit_totals"`
	Transitions       int               `json:"transitions"`
	CostUSD           float64           `json:"cost_usd"`
	Visits            []VisitSummary    `json:"visits,omitempty"`
	PendingAsk        *Ask              `json:"pending_ask,omitempty"`
	Slices            []Slice           `json:"slices,omitempty"`
	ProposedSlices    []Slice           `json:"proposed_slices,omitempty"`
	ProposedSeq       int               `json:"proposed_seq,omitempty"`
	Parent            *ParentRef        `json:"parent,omitempty"`
	Slice             *SliceRef         `json:"slice,omitempty"`
	Children          []ChildRef        `json:"children,omitempty"`
	Provider          string            `json:"provider"`
	Branch            string            `json:"branch"`
	Base              string            `json:"base,omitempty"`
	Workspace         *workspace.Lease  `json:"workspace,omitempty"`
	WorkspaceMissing  bool              `json:"workspace_missing,omitempty"`
	BaseMoved         bool              `json:"base_moved,omitempty"`
	PermissionDenials int               `json:"permission_denials,omitempty"`
	FakeAgents        string            `json:"fake_agents,omitempty"`
	ShipVersion       string            `json:"ship_version,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	FinishedAt        *time.Time        `json:"finished_at,omitempty"`
	LastEventSeq      int               `json:"last_event_seq"`
}

// Visit returns the visit with seq, or nil.
func (s *RunSnapshot) Visit(seq int) *VisitSummary {
	for i := range s.Visits {
		if s.Visits[i].Seq == seq {
			return &s.Visits[i]
		}
	}
	return nil
}

// LastVisit returns the latest visit, or nil.
func (s *RunSnapshot) LastVisit() *VisitSummary {
	if len(s.Visits) == 0 {
		return nil
	}
	return &s.Visits[len(s.Visits)-1]
}

// LastFinishedVisit returns the latest visit with an outcome, or nil.
func (s *RunSnapshot) LastFinishedVisit() *VisitSummary {
	for i := len(s.Visits) - 1; i >= 0; i-- {
		if s.Visits[i].Finished != nil {
			return &s.Visits[i]
		}
	}
	return nil
}

// Lite returns a copy without visits (for list endpoints).
func (s RunSnapshot) Lite() RunSnapshot {
	s.Visits = nil
	return s
}

const summaryLimit = 600

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Apply applies one event to the snapshot. It is the only way snapshots
// change.
func Apply(s *RunSnapshot, e Event) error {
	if e.Seq != s.LastEventSeq+1 {
		return fmt.Errorf("event seq %d out of order (last %d)", e.Seq, s.LastEventSeq)
	}
	dec := func(v any) error {
		if err := json.Unmarshal(e.Data, v); err != nil {
			return fmt.Errorf("event %d (%s): %w", e.Seq, e.Type, err)
		}
		return nil
	}
	if s.Vars == nil {
		s.Vars = map[string]string{}
	}
	if s.VisitCounts == nil {
		s.VisitCounts = map[string]int{}
	}
	if s.VisitTotals == nil {
		s.VisitTotals = map[string]int{}
	}
	ts := e.TS.UTC()
	switch e.Type {
	case EvRunCreated:
		var d RunCreated
		if err := dec(&d); err != nil {
			return err
		}
		s.ID, s.Pipeline, s.Repo, s.RepoOrigin, s.Title = d.ID, d.Pipeline, d.Repo, d.RepoOrigin, d.BriefTitle
		s.CurrentStep = d.Start
		s.Status = StatusStarting
		s.Parent, s.Slice = d.Parent, d.Slice
		s.Provider, s.Branch, s.Base = d.Provider, d.Branch, d.Base
		s.FakeAgents, s.ShipVersion = d.FakeAgents, d.ShipVersion
		for k, v := range d.Vars {
			s.Vars[k] = v
		}
		s.CreatedAt = ts
	case EvWorkspaceAcquired:
		var d WorkspaceAcquired
		if err := dec(&d); err != nil {
			return err
		}
		l := d.Lease
		s.Workspace = &l
		s.WorkspaceMissing = false
		s.Branch, s.Base = l.Branch, l.Base
	case EvWorkspaceReleased:
		s.Workspace = nil
	case EvWorkspaceMissing:
		s.WorkspaceMissing = true
	case EvVisitStarted:
		var d VisitStarted
		if err := dec(&d); err != nil {
			return err
		}
		s.VisitCounts[d.Step]++
		s.VisitTotals[d.Step] = d.VisitNumber
		s.CurrentStep = d.Step
		s.Visits = append(s.Visits, VisitSummary{
			Seq: d.Seq, Step: d.Step, Type: d.Type, VisitNumber: d.VisitNumber, CameFrom: d.CameFrom,
			Dir: d.Dir, Started: ts, SessionID: d.SessionID, ResumeID: d.ResumeID, Queued: d.Queued,
		})
	case EvVisitDequeued:
		var d SeqOnly
		if err := dec(&d); err != nil {
			return err
		}
		if v := s.Visit(d.Seq); v != nil {
			v.Queued = false
		}
	case EvVisitFinished:
		var d VisitFinished
		if err := dec(&d); err != nil {
			return err
		}
		if v := s.Visit(d.Seq); v != nil {
			t := ts
			v.Finished = &t
			v.Outcome, v.Summary, v.Error = d.Outcome, truncate(d.Summary, summaryLimit), d.Error
			v.CostUSD, v.DurationMS = d.CostUSD, d.DurationMS
			v.Queued = false
			v.PermissionDenials = d.PermissionDenials
			if d.SessionID != "" {
				v.SessionID = d.SessionID
			}
		}
		s.CostUSD += d.CostUSD
		s.PermissionDenials += d.PermissionDenials
		if s.PendingAsk != nil && s.PendingAsk.Seq == d.Seq {
			s.PendingAsk = nil
		}
	case EvVisitInterrupted:
		var d SeqOnly
		if err := dec(&d); err != nil {
			return err
		}
		if v := s.Visit(d.Seq); v != nil {
			v.Interrupted = true
			v.Queued = false
		}
		if s.PendingAsk != nil && s.PendingAsk.Seq == d.Seq {
			s.PendingAsk = nil
		}
	case EvTransition:
		var d Transition
		if err := dec(&d); err != nil {
			return err
		}
		s.Transitions++
		s.CurrentStep, s.CameFrom = d.To, d.From
		if d.Reset {
			s.VisitCounts[d.To] = 0
		}
	case EvVarSet:
		var d VarSet
		if err := dec(&d); err != nil {
			return err
		}
		s.Vars[d.Name] = d.Value
	case EvAskPending:
		var d AskPending
		if err := dec(&d); err != nil {
			return err
		}
		s.PendingAsk = &Ask{Seq: d.Seq, Kind: d.Kind, Question: d.Question, Choices: d.Choices, Input: d.Input, Show: d.Show, Var: d.Var, Since: ts}
	case EvAskAnswered:
		s.PendingAsk = nil
	case EvWaitPolled:
		var d WaitPolled
		if err := dec(&d); err != nil {
			return err
		}
		if v := s.Visit(d.Seq); v != nil {
			v.Polls, v.LastPoll = d.N, d.LastLine
		}
	case EvSlicesProposed:
		var d SlicesProposed
		if err := dec(&d); err != nil {
			return err
		}
		s.ProposedSlices, s.ProposedSeq = d.Slices, d.Seq
	case EvSlicesApproved:
		var d SeqOnly
		if err := dec(&d); err != nil {
			return err
		}
		if d.Seq == s.ProposedSeq {
			s.Slices = append([]Slice(nil), s.ProposedSlices...)
		}
	case EvChildStarted:
		var d ChildStarted
		if err := dec(&d); err != nil {
			return err
		}
		s.Children = append(s.Children, ChildRef{ID: d.ChildID, SliceKey: d.SliceKey, Number: d.Number, Status: StatusStarting})
	case EvChildFinished:
		var d ChildFinished
		if err := dec(&d); err != nil {
			return err
		}
		for i := range s.Children {
			if s.Children[i].ID == d.ChildID {
				s.Children[i].Status = d.Status
			}
		}
	case EvBaseMoved:
		s.BaseMoved = true
	case EvStatusChanged:
		var d StatusChanged
		if err := dec(&d); err != nil {
			return err
		}
		s.Status, s.StatusReason = d.To, d.Reason
	case EvCommand:
		// Recorded for audit; the effects arrive as their own events.
	case EvRunFinished:
		var d RunFinished
		if err := dec(&d); err != nil {
			return err
		}
		s.Status, s.StatusReason = d.Status, d.Reason
		t := ts
		s.FinishedAt = &t
		s.PendingAsk = nil
	default:
		// Unknown types are kept in the log and ignored, for forward compatibility.
	}
	s.LastEventSeq = e.Seq
	s.UpdatedAt = ts
	return nil
}
