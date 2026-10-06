package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
)

// RunStats is a finished run's record in the pipeline's history
// (runs/<run-id>.json), written once when the run ends. Feedback on the
// run is joined in when read, since it can arrive later.
type RunStats struct {
	Schema      int              `json:"schema"`
	Run         string           `json:"run"`
	Pipeline    string           `json:"pipeline"`
	Version     string           `json:"version,omitempty"` // the version hash the run ended on
	ParentRun   string           `json:"parent_run,omitempty"`
	Status      store.Status     `json:"status"`
	Reason      string           `json:"reason,omitempty"`
	Started     time.Time        `json:"started"`
	Finished    time.Time        `json:"finished"`
	DurationMS  int64            `json:"duration_ms"`
	CostUSD     float64          `json:"cost_usd"`
	Tokens      int64            `json:"tokens"`
	Usage       store.TokenUsage `json:"usage"`
	Transitions int              `json:"transitions"`
	Upgrades    int              `json:"upgrades,omitempty"`
	Steps       []StepStats      `json:"steps"`
	Human       Human            `json:"human"`
	PR          *PRStats         `json:"pr,omitempty"`

	// Joined when read: feedback recorded against the run.
	Feedback   int `json:"feedback,omitempty"`
	PRFeedback int `json:"pr_feedback,omitempty"` // of which "ship:" PR comments
}

// StepStats is one step's part in a run.
type StepStats struct {
	Step        string           `json:"step"`
	Type        string           `json:"type"`
	Visits      int              `json:"visits"`
	Outcomes    map[string]int   `json:"outcomes,omitempty"`
	DurationsMS []int64          `json:"durations_ms,omitempty"`
	CostUSD     float64          `json:"cost_usd,omitempty"`
	Tokens      int64            `json:"tokens,omitempty"`
	Usage       store.TokenUsage `json:"usage"`
	Errors      map[string]int   `json:"errors,omitempty"`  // by reason
	Choices     map[string]int   `json:"choices,omitempty"` // answers at a check-in
	Human       Human            `json:"human"`
}

// Human is how much a person had to do.
type Human struct {
	// CheckIns are questions answered: asks, split reviews and variable
	// prompts, by kind.
	CheckIns     int            `json:"check_ins,omitempty"`
	CheckInKinds map[string]int `json:"check_in_kinds,omitempty"`
	// WaitMS is time spent waiting for a person (asking or needing
	// attention, until someone acted).
	WaitMS int64 `json:"wait_ms,omitempty"`
	// Interventions are manual controls (retry, goto, upgrade…) by name.
	Interventions map[string]int `json:"interventions,omitempty"`
	NotesStep     int            `json:"notes_step,omitempty"`
	NotesRun      int            `json:"notes_run,omitempty"`
	// Autonomous: nobody answered or stepped in before the run reached a
	// pr step (or at all, without one). Scheduling (pause, resume) doesn't
	// count. Only set on the run.
	Autonomous bool `json:"autonomous,omitempty"`
}

// Scheduling commands: a person managing their own time, not the run
// needing help, so they're left out of HandsOn and Autonomous.
var scheduling = map[string]bool{"pause": true, "resume": true}

// Corrective counts interventions other than scheduling ones.
func (h Human) Corrective() int {
	n := 0
	for k, c := range h.Interventions {
		if !scheduling[k] {
			n += c
		}
	}
	return n
}

// HandsOn is check-ins plus corrective interventions: the times a person
// had to act. Lower is better.
func (h Human) HandsOn() int { return h.CheckIns + h.Corrective() }

// Needed reports whether a person was involved at all.
func (h Human) Needed() bool { return h.HandsOn() > 0 || h.WaitMS > 0 }

// PRStats is what a run's pr steps saw.
type PRStats struct {
	FeedbackBatches int  `json:"feedback_batches"` // review feedback sent to a fixer
	Comments        int  `json:"comments"`         // comments in those batches
	CIFailures      int  `json:"ci_failures"`
	Rounds          int  `json:"rounds"` // batches and CI failures before ready or merged
	Ready           bool `json:"ready,omitempty"`
	Merged          bool `json:"merged,omitempty"`
}

// RunStatsSchema is the current RunStats layout.
const RunStatsSchema = 1

var piecesRE = regexp.MustCompile(`^(\d+) new piece`)

func (h *Human) intervene(name string) {
	if h.Interventions == nil {
		h.Interventions = map[string]int{}
	}
	h.Interventions[name]++
}

func (h *Human) checkIn(kind string) {
	if h.CheckInKinds == nil {
		h.CheckInKinds = map[string]int{}
	}
	h.CheckIns++
	h.CheckInKinds[kind]++
}

func (h *Human) undoIntervention(name string) {
	if h.Interventions[name] > 0 {
		h.Interventions[name]--
		if h.Interventions[name] == 0 {
			delete(h.Interventions, name)
		}
	}
}

// BuildRunStats derives a finished run's stats from its snapshot and
// event log.
func BuildRunStats(s *store.RunSnapshot, events []store.Event) RunStats {
	rs := RunStats{
		Schema: RunStatsSchema, Run: s.ID, Pipeline: s.Pipeline, Version: s.PipelineHash,
		Status: s.Status, Reason: s.StatusReason, Started: s.CreatedAt,
		CostUSD: s.CostUSD, Tokens: s.Tokens, Transitions: s.Transitions, Upgrades: s.Upgrades,
	}
	if s.Parent != nil {
		rs.ParentRun = s.Parent.ID
	}
	if s.FinishedAt != nil {
		rs.Finished = *s.FinishedAt
	} else {
		rs.Finished = s.UpdatedAt
	}
	rs.DurationMS = rs.Finished.Sub(rs.Started).Milliseconds()

	idx := map[string]int{}
	step := func(name, typ string) *StepStats {
		i, ok := idx[name]
		if !ok {
			i = len(rs.Steps)
			idx[name] = i
			rs.Steps = append(rs.Steps, StepStats{Step: name, Type: typ})
		}
		if typ != "" && rs.Steps[i].Type == "" {
			rs.Steps[i].Type = typ
		}
		return &rs.Steps[i]
	}
	var pr *PRStats
	prDone := false
	firstPR := -1 // seq of the first pr visit
	for _, v := range s.Visits {
		st := step(v.Step, v.Type)
		if v.Type == "pr" && firstPR < 0 {
			firstPR = v.Seq
		}
		if v.Finished == nil {
			continue
		}
		st.Visits++
		if st.Outcomes == nil {
			st.Outcomes = map[string]int{}
		}
		st.Outcomes[v.Outcome]++
		st.DurationsMS = append(st.DurationsMS, v.DurationMS)
		st.CostUSD += v.CostUSD
		st.Tokens += v.Tokens
		if v.Usage != nil {
			st.Usage.Add(*v.Usage)
			rs.Usage.Add(*v.Usage)
		}
		if v.Error != nil {
			if st.Errors == nil {
				st.Errors = map[string]int{}
			}
			st.Errors[v.Error.Reason]++
		}
		if v.Type == "pr" {
			if pr == nil {
				pr = &PRStats{}
			}
			switch v.Outcome {
			case "feedback":
				pr.FeedbackBatches++
				if m := piecesRE.FindStringSubmatch(v.Summary); m != nil {
					n, _ := strconv.Atoi(m[1])
					pr.Comments += n
				}
				if !prDone {
					pr.Rounds++
				}
			case "ci_failed":
				pr.CIFailures++
				if !prDone {
					pr.Rounds++
				}
			case "ready":
				pr.Ready, prDone = true, true
			case "merged":
				pr.Merged, pr.Ready, prDone = true, true, true
			}
		}
	}
	rs.PR = pr

	// Human involvement, from the event log.
	visitStep := map[int]string{}
	pendingKind := map[int]string{}
	cur := ""
	var waitFrom time.Time
	waitStep := ""
	beforePR := true
	autonomous := true
	var prevCmd *store.Command
	prevCmdStep := ""
	for _, ev := range events {
		thisCmd := (*store.Command)(nil)
		switch ev.Type {
		case store.EvRunCreated:
			var d store.RunCreated
			if json.Unmarshal(ev.Data, &d) == nil {
				cur = d.Start
			}
		case store.EvTransition:
			var d store.Transition
			if json.Unmarshal(ev.Data, &d) == nil {
				cur = d.To
			}
		case store.EvVisitStarted:
			var d store.VisitStarted
			if json.Unmarshal(ev.Data, &d) == nil {
				visitStep[d.Seq] = d.Step
				cur = d.Step
				if firstPR >= 0 && d.Seq >= firstPR {
					beforePR = false
				}
			}
		case store.EvAskPending:
			var d store.AskPending
			if json.Unmarshal(ev.Data, &d) == nil {
				pendingKind[d.Seq] = d.Kind
			}
		case store.EvStatusChanged:
			var d store.StatusChanged
			if json.Unmarshal(ev.Data, &d) != nil {
				break
			}
			if d.To.InInbox() && waitFrom.IsZero() {
				waitFrom, waitStep = ev.TS, cur
			} else if !d.To.InInbox() && !waitFrom.IsZero() {
				ms := ev.TS.Sub(waitFrom).Milliseconds()
				rs.Human.WaitMS += ms
				if waitStep != "" {
					step(waitStep, "").Human.WaitMS += ms
				}
				waitFrom = time.Time{}
			}
		case store.EvAskAnswered:
			var d store.AskAnswered
			if json.Unmarshal(ev.Data, &d) != nil || d.Choice == "skipped" {
				break
			}
			name := visitStep[d.Seq]
			if name == "" {
				name = cur
			}
			kind := pendingKind[d.Seq]
			if kind == "" {
				kind = store.AskKindAsk
			}
			st := step(name, "")
			rs.Human.checkIn(kind)
			st.Human.checkIn(kind)
			if d.Choice != "" {
				if st.Choices == nil {
					st.Choices = map[string]int{}
				}
				st.Choices[d.Choice]++
			}
			if d.Note != "" && kind != store.AskKindVar {
				if d.For == store.NoteForRun {
					rs.Human.NotesRun++
					st.Human.NotesRun++
				} else {
					rs.Human.NotesStep++
					st.Human.NotesStep++
				}
			}
			// The command that gave the answer was the check-in, not an
			// intervention (unless it also cancelled the run).
			if prevCmd != nil && prevCmd.Name != "cancel" {
				rs.Human.undoIntervention(prevCmd.Name)
				step(prevCmdStep, "").Human.undoIntervention(prevCmd.Name)
			}
			if beforePR {
				autonomous = false
			}
		case store.EvCommand:
			var d store.Command
			if json.Unmarshal(ev.Data, &d) != nil || d.Source == "engine" || ev.Actor != store.ActorUser {
				break
			}
			if d.Name == "answer" || d.Name == "split_review" {
				break // counted by the answer itself
			}
			rs.Human.intervene(d.Name)
			step(cur, "").Human.intervene(d.Name)
			if beforePR && !scheduling[d.Name] {
				autonomous = false
			}
			thisCmd, prevCmdStep = &d, cur
		}
		if ev.Type != store.EvCommand || thisCmd != nil {
			prevCmd = thisCmd
		}
	}
	if !waitFrom.IsZero() {
		ms := rs.Finished.Sub(waitFrom).Milliseconds()
		if ms > 0 {
			rs.Human.WaitMS += ms
			step(waitStep, "").Human.WaitMS += ms
		}
	}
	rs.Human.Autonomous = autonomous
	// Drop steps that never ran and nobody touched.
	kept := rs.Steps[:0]
	for _, st := range rs.Steps {
		if st.Visits > 0 || st.Human.Needed() || st.Human.Interventions != nil {
			kept = append(kept, st)
		}
	}
	rs.Steps = kept
	return rs
}

// WriteRun records a finished run's stats (once: an existing record is
// kept).
func (s *Store) WriteRun(rs RunStats) error {
	if rs.Run == "" || strings.ContainsAny(rs.Run, `/\`) {
		return os.ErrInvalid
	}
	return writeOnce(filepath.Join(s.Dir, RunsDir, rs.Run+".json"), rs)
}

// HasRun reports whether a run's stats are recorded.
func (s *Store) HasRun(id string) bool {
	_, err := os.Stat(filepath.Join(s.Dir, RunsDir, id+".json"))
	return err == nil
}

// Runs returns the recorded runs, oldest first, with the feedback on each
// joined in.
func (s *Store) Runs() ([]RunStats, error) {
	paths, err := filepath.Glob(filepath.Join(s.Dir, RunsDir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []RunStats
	for _, p := range paths {
		rs, err := readJSON[RunStats](p)
		if err != nil || rs.Run == "" {
			continue
		}
		out = append(out, rs)
	}
	if len(out) == 0 {
		return out, nil
	}
	fb, _ := s.feedbackRecords()
	byRun := map[string]*RunStats{}
	for i := range out {
		byRun[out[i].Run] = &out[i]
	}
	for _, f := range fb {
		if r := byRun[f.Run]; f.Type == TypeFeedback && r != nil {
			r.Feedback++
			if f.Source == FromPR {
				r.PRFeedback++
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out, nil
}

// AverageSteps averages each step across runs, per run (a run that
// skipped a step counts as zero), in the order steps first ran.
func AverageSteps(runs []RunStats) []store.StepAverage {
	var out []store.StepAverage
	idx := map[string]int{}
	for _, r := range runs {
		for _, st := range r.Steps {
			if st.Visits == 0 {
				continue
			}
			i, ok := idx[st.Step]
			if !ok {
				i = len(out)
				idx[st.Step] = i
				out = append(out, store.StepAverage{Step: st.Step, Type: st.Type})
			}
			a := &out[i]
			a.Runs++
			a.Visits += float64(st.Visits)
			for _, d := range st.DurationsMS {
				a.DurationMS += d
			}
			a.CostUSD += st.CostUSD
			a.Tokens += st.Tokens
			a.Usage.Add(st.Usage)
		}
	}
	n := int64(len(runs))
	if n == 0 {
		return out
	}
	for i := range out {
		a := &out[i]
		a.Visits /= float64(n)
		a.DurationMS /= n
		a.CostUSD /= float64(n)
		a.Tokens /= n
		a.Usage = store.TokenUsage{Input: a.Usage.Input / n, Output: a.Usage.Output / n, CacheWrite: a.Usage.CacheWrite / n, CacheRead: a.Usage.CacheRead / n}
	}
	return out
}
