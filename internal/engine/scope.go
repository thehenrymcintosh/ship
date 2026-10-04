package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/merlin-digital/ship/internal/brief"
	"github.com/merlin-digital/ship/internal/engine/steps"
	"github.com/merlin-digital/ship/internal/pipeline"
	"github.com/merlin-digital/ship/internal/store"
	"github.com/merlin-digital/ship/internal/tmpl"
)

// scopeInput is what a template scope is built from.
type scopeInput struct {
	pipe        *pipeline.Pipeline
	snap        *store.RunSnapshot
	runDir      string
	brief       *brief.Brief
	parent      *store.RunSnapshot
	parentDir   string
	parentBrief *brief.Brief
	prev        *store.VisitSummary
	cameFrom    string
	visitNumber int
	step        string
	seq         int
}

func setRun(s *tmpl.Scope, prefix string, snap *store.RunSnapshot, dir string) {
	s.Set(prefix+"run.id", snap.ID)
	s.Set(prefix+"run.dir", dir)
	wt := ""
	if snap.Workspace != nil {
		wt = snap.Workspace.Path
	}
	s.Set(prefix+"run.worktree", wt)
	s.Set(prefix+"run.branch", snap.Branch)
	s.Set(prefix+"run.base", snap.Base)
	s.Set(prefix+"run.pipeline", snap.Pipeline)
	s.Set(prefix+"run.repo", snap.Repo)
}

func setBrief(s *tmpl.Scope, prefix, dir string, b *brief.Brief) {
	s.Set(prefix+"brief.path", filepath.Join(dir, store.BriefFile))
	if b != nil {
		s.Set(prefix+"brief.title", b.Title)
		s.Set(prefix+"brief.acceptance", b.AcceptanceMarkdown())
	}
}

// buildScope assembles the placeholder values for a visit.
func buildScope(in scopeInput) *tmpl.Scope {
	var declared []string
	for name := range in.pipe.Variables {
		declared = append(declared, name)
	}
	s := tmpl.NewScope(declared)
	for _, name := range declared {
		if v, ok := in.snap.Vars[name]; ok {
			s.Set("vars."+name, v)
		} else {
			s.MarkUnset("vars." + name)
		}
	}
	setBrief(s, "", in.runDir, in.brief)
	setRun(s, "", in.snap, in.runDir)
	if in.prev != nil {
		s.Set("prev.step", in.prev.Step)
		s.Set("prev.outcome", in.prev.Outcome)
		s.Set("prev.summary", in.prev.Summary)
		s.Set("prev.handover", filepath.Join(in.runDir, store.VisitsDir, in.prev.Dir, "handover.md"))
	}
	s.Set("came_from", in.cameFrom)
	if in.visitNumber > 0 {
		s.Set("visit.number", strconv.Itoa(in.visitNumber))
	}
	if sl := in.snap.Slice; sl != nil {
		s.Set("slice.key", sl.Key)
		s.Set("slice.number", strconv.Itoa(sl.Number))
		s.Set("slice.title", sl.Title)
		s.Set("slice.count", strconv.Itoa(sl.Count))
	}
	if in.parent != nil {
		setRun(s, "parent.", in.parent, in.parentDir)
		setBrief(s, "parent.", in.parentDir, in.parentBrief)
		for k, v := range in.parent.Vars {
			s.Set("parent.vars."+k, v)
		}
	}
	s.Set(tmpl.StepKey, in.step)
	if in.seq > 0 {
		s.Set(tmpl.SeqKey, strconv.Itoa(in.seq))
	}
	return s
}

// stepEnv renders a step's env map.
func stepEnv(st *pipeline.Step, scope *tmpl.Scope) ([]string, error) {
	keys := make([]string, 0, len(st.Env))
	for k := range st.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		v, err := tmpl.Render(st.Env[k], scope, tmpl.Plain)
		if err != nil {
			return nil, err
		}
		out = append(out, k+"="+v)
	}
	return out, nil
}

// visitDirName is "NNNN-<step>".
func visitDirName(seq int, step string) string { return fmt.Sprintf("%04d-%s", seq, step) }

// writeHandover writes handover.md.
func writeHandover(path string, runID, step string, number int, res steps.Result, finished time.Time, tail int) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Handover: %s (visit %d) → `%s`\n\n", step, number, res.Outcome)
	fmt.Fprintf(&b, "**Run:** %s · **Step:** %s · **Finished:** %s", runID, step, finished.Local().Format("2006-01-02 15:04:05"))
	if res.Cost > 0 {
		fmt.Fprintf(&b, " · **Cost:** $%.2f", res.Cost)
	}
	b.WriteString("\n\n## Summary\n")
	summary := strings.TrimSpace(res.Summary)
	if summary == "" {
		summary = "(none)"
	}
	b.WriteString(summary + "\n")
	if res.Error != nil {
		fmt.Fprintf(&b, "\n## Error\n**Reason:** `%s`\n\n%s\n", res.Error.Reason, res.Error.Message)
	}
	if len(res.Vars) > 0 {
		b.WriteString("\n## Variables set\n")
		keys := make([]string, 0, len(res.Vars))
		for k := range res.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s = %q\n", k, res.Vars[k])
		}
	}
	if len(res.PermissionDenials) > 0 {
		fmt.Fprintf(&b, "\n## Permission denials\n%d tool call(s) were denied. The agent may have been blocked; consider allowed_tools.\n", len(res.PermissionDenials))
	}
	if len(res.Output) > 0 {
		lines := res.Output
		if tail > 0 && len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
		fmt.Fprintf(&b, "\n## Output (last %d lines)\n```\n%s\n```\n", len(lines), strings.Join(lines, "\n"))
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// resultJSON is result.json.
type resultJSON struct {
	Seq               int               `json:"seq"`
	Step              string            `json:"step"`
	Type              string            `json:"type"`
	Outcome           string            `json:"outcome"`
	Summary           string            `json:"summary"`
	Vars              map[string]string `json:"vars,omitempty"`
	Error             *store.StepError  `json:"error,omitempty"`
	ExitCode          *int              `json:"exit_code,omitempty"`
	CostUSD           float64           `json:"cost_usd,omitempty"`
	Usage             json.RawMessage   `json:"usage,omitempty"`
	SessionID         string            `json:"session_id,omitempty"`
	DurationMS        int64             `json:"duration_ms"`
	PermissionDenials []json.RawMessage `json:"permission_denials,omitempty"`
	Polls             int               `json:"polls,omitempty"`
	Extra             json.RawMessage   `json:"extra,omitempty"`
}

func writeResult(path string, r resultJSON) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(path, append(b, '\n'), 0o600)
}
