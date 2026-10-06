package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// CheckInSummaryFile caches a check-in's generated summary in its visit dir.
const CheckInSummaryFile = "checkin-summary.json"

// CheckInSummary is a check-in's decision as a small model wrote it from
// the previous step's handover, when that step's agent gave none.
type CheckInSummary struct {
	agent.Decision
	Model   string  `json:"model"`
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// SummaryModel is the model that summarises check-ins.
const SummaryModel = "haiku"

// ReadDecision reads the decision an agent reported in a visit's
// result.json, or nil.
func ReadDecision(visitDir string) *agent.Decision {
	b, err := os.ReadFile(filepath.Join(visitDir, "result.json"))
	if err != nil {
		return nil
	}
	var r struct {
		Decision *agent.Decision `json:"decision"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return r.Decision
}

// ReadCheckInSummary reads a check-in's cached summary, or nil.
func ReadCheckInSummary(visitDir string) *CheckInSummary {
	b, err := os.ReadFile(filepath.Join(visitDir, CheckInSummaryFile))
	if err != nil {
		return nil
	}
	var s CheckInSummary
	if json.Unmarshal(b, &s) != nil || s.Headline == "" {
		return nil
	}
	return &s
}

// summaryPrev is the visit a check-in summary is about: the last finished
// visit before the check-in, when it was an agent's that ended cleanly but
// gave no decision. Anything else either has its own decision or is a
// failure ship diagnoses itself.
func summaryPrev(v *Visit) *store.VisitSummary {
	s := v.Snapshot
	if s == nil {
		return nil
	}
	for i := len(s.Visits) - 1; i >= 0; i-- {
		pv := &s.Visits[i]
		if pv.Seq >= v.Seq || pv.Finished == nil {
			continue
		}
		if (pv.Type != pipeline.TypeAgent && pv.Type != pipeline.TypeSplit) || pv.Error != nil || pv.PermissionDenials > 0 {
			return nil
		}
		if ReadDecision(filepath.Join(v.RunDir, store.VisitsDir, pv.Dir)) != nil {
			return nil
		}
		return pv
	}
	return nil
}

// summarizeCheckIn has a small model turn the previous step's handover into
// a decision for the check-in, once (the result is cached in the visit dir),
// and adds its cost to the run's. Failures are only logged: the card falls
// back to the question and the handover.
func summarizeCheckIn(ctx context.Context, v *Visit, question string) {
	if !v.Summarize {
		return
	}
	if _, err := os.Stat(v.File(CheckInSummaryFile)); err == nil {
		return
	}
	pv := summaryPrev(v)
	if pv == nil {
		return
	}
	handover, err := os.ReadFile(filepath.Join(v.RunDir, store.VisitsDir, pv.Dir, "handover.md"))
	if err != nil {
		return
	}
	cli := v.ForceCLI
	if cli == "" {
		cli = "claude"
	}
	ad, err := v.RT.Agents().Get(cli)
	if err != nil {
		return
	}
	choices := v.Step.Choices.Keys()
	schema, _ := json.Marshal(agent.DecisionSchema(choices))
	var b strings.Builder
	fmt.Fprintf(&b, "A person is being asked, at a check-in of an automated coding pipeline: %q\n", question)
	fmt.Fprintf(&b, "Their choices: %s.\n\n", strings.Join(choices, ", "))
	fmt.Fprintf(&b, "Below is the handover from the step before (%q, outcome %q). Using only what it says, report a decision for them, most important first: ", pv.Step, pv.Outcome)
	b.WriteString("headline (one line: what they're deciding), situation (2-3 short lines: what happened and what's at stake), options (each choice and what picking it does), recommended (the choice the handover supports) and reason (one line). Plain, specific words; no preamble. Don't use any tools.\n\n<handover>\n")
	b.WriteString(agent.TruncateHandover(string(handover), "handover.md"))
	b.WriteString("\n</handover>\n")

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := ad.Run(ctx, agent.Request{
		Workdir: v.Dir, Prompt: b.String(), Model: SummaryModel,
		Permission:   agent.Permission{Mode: "default"},
		OutputSchema: schema, BudgetUSD: 0.25, Timeout: 2 * time.Minute,
		Env: v.Env, TranscriptW: io.Discard, StderrW: io.Discard,
		RunID: v.RunID, Step: v.StepName + ".summary", VisitNumber: v.Number,
	}, nil)
	// The event adds the cost to the run's, and tells the UI to look again.
	spent := func() {
		_ = v.RT.Emit(store.EvCostAdded, store.CostAdded{Seq: v.Seq, What: "check-in summary", CostUSD: resp.CostUSD, Tokens: resp.Tokens})
	}
	var d agent.Decision
	if err != nil || resp.IsError || agent.ValidateOutput(schema, resp.Structured) != nil ||
		json.Unmarshal(resp.Structured, &d) != nil || d.Validate(choices) != nil {
		if resp.CostUSD > 0 || resp.Tokens > 0 {
			spent()
		}
		return
	}
	out, _ := json.MarshalIndent(CheckInSummary{Decision: d, Model: SummaryModel, CostUSD: resp.CostUSD}, "", "  ")
	_ = store.WriteFileAtomic(v.File(CheckInSummaryFile), append(out, '\n'), 0o600)
	spent()
}
