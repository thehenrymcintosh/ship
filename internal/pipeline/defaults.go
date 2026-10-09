package pipeline

import (
	"sort"
	"strconv"
	"time"
)

// Built-in defaults.
const (
	DefaultMaxVisits      = 5
	DefaultTimeout        = 30 * time.Minute
	DefaultWaitTimeout    = 24 * time.Hour
	DefaultPRTimeout      = 14 * 24 * time.Hour
	DefaultSettle         = 10 * time.Minute
	DefaultEvery          = time.Minute
	DefaultOutputTail     = 200
	DefaultMaxTransitions = 200
	DefaultMaxSlices      = 8
	DefaultMaxParallel    = 3
)

// MaxVisits returns the effective max_visits of a step (0 = unlimited).
func (p *Pipeline) MaxVisits(s *Step) int {
	if s.MaxVisits != nil {
		return *s.MaxVisits
	}
	if p.Defaults != nil && p.Defaults.MaxVisits != nil {
		return *p.Defaults.MaxVisits
	}
	return DefaultMaxVisits
}

// WhenExhausted returns the effective when_exhausted target ("" = park).
func (p *Pipeline) WhenExhausted(s *Step) string {
	if s.WhenExhausted != "" {
		return s.WhenExhausted
	}
	if p.Defaults != nil {
		return p.Defaults.WhenExhausted
	}
	return ""
}

// OnError returns the target for an `error` outcome: an explicit `next.error`,
// the step's on_error, or the default ("" = park).
func (p *Pipeline) OnError(s *Step) string {
	if s.Next != nil && s.Next.IsMap {
		if t, ok := s.Next.Map.Get(OutcomeError); ok {
			return t
		}
	}
	if s.OnError != "" {
		return s.OnError
	}
	if p.Defaults != nil {
		return p.Defaults.OnError
	}
	return ""
}

// Timeout returns the per-visit timeout; 0 means none (ask, fanout).
func (p *Pipeline) Timeout(s *Step) time.Duration {
	switch s.Type() {
	case TypeAsk, TypeFanout:
		return 0
	case TypeWait:
		return s.Timeout.D(DefaultWaitTimeout)
	case TypePR:
		return s.Timeout.D(DefaultPRTimeout)
	}
	if s.Timeout != nil {
		return time.Duration(*s.Timeout)
	}
	if p.Defaults != nil && p.Defaults.Timeout != nil {
		return time.Duration(*p.Defaults.Timeout)
	}
	return DefaultTimeout
}

// OutputTail returns how many output lines handovers include.
func (p *Pipeline) OutputTail() int {
	if p.Defaults != nil && p.Defaults.OutputTail != nil {
		return *p.Defaults.OutputTail
	}
	return DefaultOutputTail
}

// MaxTransitions returns the run's transition limit.
func (p *Pipeline) MaxTransitions() int {
	if p.Limits != nil && p.Limits.MaxTransitions != nil {
		return *p.Limits.MaxTransitions
	}
	return DefaultMaxTransitions
}

// MaxTokens returns the run's token budget (0 = none).
func (p *Pipeline) MaxTokens() int64 {
	if p.Limits == nil {
		return 0
	}
	return p.Limits.MaxTokens.N()
}

// MaxBudget returns the run's budget in USD (0 = none).
func (p *Pipeline) MaxBudget() float64 {
	if p.Limits != nil && p.Limits.MaxBudgetUSD != nil {
		return *p.Limits.MaxBudgetUSD
	}
	return 0
}

// AgentFor merges agent settings: base (config) < pipeline < step.
func (p *Pipeline) AgentFor(base AgentConfig, s *Step) AgentConfig {
	out := base
	if p.Agent != nil {
		out = mergeAgent(out, *p.Agent)
	}
	return mergeAgent(out, s.AgentConfig)
}

func mergeAgent(a, b AgentConfig) AgentConfig {
	if b.CLI != "" {
		a.CLI = b.CLI
	}
	if b.Model != "" {
		a.Model = b.Model
	}
	if b.Effort != "" {
		a.Effort = b.Effort
	}
	if b.PermissionMode != "" {
		a.PermissionMode = b.PermissionMode
	}
	if b.AllowedTools != nil {
		a.AllowedTools = b.AllowedTools
	}
	if b.DisallowedTools != nil {
		a.DisallowedTools = b.DisallowedTools
	}
	if b.ExtraArgs != nil {
		a.ExtraArgs = b.ExtraArgs
	}
	if b.Lean != nil {
		a.Lean = b.Lean
	}
	if b.Tools != nil {
		a.Tools = b.Tools
	}
	if b.MCPConfig != nil {
		a.MCPConfig = b.MCPConfig
	}
	if b.MCP != nil {
		a.MCP = b.MCP
	}
	if b.FreshAfterIdle != nil {
		a.FreshAfterIdle = b.FreshAfterIdle
	}
	if b.FreshAfterTokens != nil {
		a.FreshAfterTokens = b.FreshAfterTokens
	}
	return a
}

// Every returns a wait step's poll interval.
func (s *Step) EveryD() time.Duration { return s.Every.D(DefaultEvery) }

// MaxSlicesN returns the split step's slice limit.
func (s *Step) MaxSlicesN() int {
	if s.MaxSlices > 0 {
		return s.MaxSlices
	}
	return DefaultMaxSlices
}

// ModeOrDefault returns the fanout mode.
func (s *Step) ModeOrDefault() string {
	if s.Mode == "" {
		return "series"
	}
	return s.Mode
}

// MaxParallelN returns the fanout parallelism.
func (s *Step) MaxParallelN() int {
	if s.MaxParallel > 0 {
		return s.MaxParallel
	}
	return DefaultMaxParallel
}

// OnChildStopOrDefault returns halt or continue.
func (s *Step) OnChildStopOrDefault() string {
	if s.OnChildStop == "" {
		return "halt"
	}
	return s.OnChildStop
}

// SettleD is how long a pr step waits after the newest comment before
// sending a batch of feedback.
func (s *Step) SettleD() time.Duration { return s.Settle.D(DefaultSettle) }

// TriggerOrDefault is auto or manual.
func (s *Step) TriggerOrDefault() string {
	if s.Trigger == "" {
		return "auto"
	}
	return s.Trigger
}

// InputOrDefault returns the ask input mode.
func (s *Step) InputOrDefault() string {
	if s.Input == "" {
		return "optional"
	}
	return s.Input
}

// OutcomeNames returns the outcome names a step can produce, excluding `error`.
func (s *Step) OutcomeNames() []string {
	switch s.Type() {
	case TypeAsk:
		return s.Choices.Keys()
	case TypeFanout:
		return []string{"done", "failed"}
	case TypeRun:
		if len(s.Outcomes) > 0 {
			seen := map[string]bool{}
			var out []string
			for _, kv := range s.Outcomes {
				if !seen[kv.Value] {
					seen[kv.Value] = true
					out = append(out, kv.Value)
				}
			}
			if _, ok := s.Outcomes.Get("default"); !ok && !seen["fail"] {
				out = append(out, "fail")
			}
			return out
		}
		return []string{"pass", "fail"}
	}
	if s.Next == nil || !s.Next.IsMap {
		if s.Type() == TypeWait || s.Type() == TypePR {
			return nil
		}
		return []string{"done"}
	}
	var out []string
	for _, k := range s.Next.Map.Keys() {
		if k != OutcomeError {
			out = append(out, k)
		}
	}
	return out
}

// RunOutcome maps a run step's exit code to an outcome name.
func (s *Step) RunOutcome(code int) string {
	if len(s.Outcomes) > 0 {
		if o, ok := s.Outcomes.Get(strconv.Itoa(code)); ok {
			return o
		}
		if o, ok := s.Outcomes.Get("default"); ok {
			return o
		}
		if code == 0 {
			return "pass"
		}
		return "fail"
	}
	if code == 0 {
		return "pass"
	}
	return "fail"
}

// Target returns the static target for a (non-error) outcome, and whether
// the step maps it.
func (s *Step) Target(outcome string) (string, bool) {
	if s.Type() == TypeAsk {
		return s.Choices.Get(outcome)
	}
	if s.Next == nil {
		return TargetDone, outcome != OutcomeError
	}
	if !s.Next.IsMap {
		return s.Next.Target, outcome != OutcomeError
	}
	return s.Next.Map.Get(outcome)
}

// Edge is a possible transition, for graphs and reachability.
type Edge struct {
	From, To string
	Label    string // outcome, "error" or "exhausted"
	Kind     string // "outcome", "error", "exhausted"
}

// Edges returns every static edge of the pipeline. `$came_from` targets are
// kept as-is; ExpandCameFrom resolves them.
func (p *Pipeline) Edges() []Edge {
	var out []Edge
	for _, name := range p.SortedSteps() {
		s := p.Steps[name]
		for _, o := range s.OutcomeNames() {
			if t, ok := s.Target(o); ok && t != "" {
				out = append(out, Edge{From: name, To: t, Label: o, Kind: "outcome"})
			}
		}
		if t := p.OnError(s); t != "" {
			out = append(out, Edge{From: name, To: t, Label: OutcomeError, Kind: "error"})
		}
		if t := p.WhenExhausted(s); t != "" && p.MaxVisits(s) > 0 {
			out = append(out, Edge{From: name, To: t, Label: "exhausted", Kind: "exhausted"})
		}
	}
	return out
}

// ExpandCameFrom replaces `$came_from` edges with edges back to every step
// that can route into the edge's source.
func ExpandCameFrom(edges []Edge) []Edge {
	preds := map[string][]string{}
	for _, e := range edges {
		if e.To != TargetCameFrom {
			preds[e.To] = append(preds[e.To], e.From)
		}
	}
	var out []Edge
	for _, e := range edges {
		if e.To != TargetCameFrom {
			out = append(out, e)
			continue
		}
		for _, from := range uniq(preds[e.From]) {
			out = append(out, Edge{From: e.From, To: from, Label: e.Label, Kind: e.Kind})
		}
	}
	return out
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// SortedSteps returns step names in file order (or sorted, if unknown).
func (p *Pipeline) SortedSteps() []string {
	if len(p.StepOrder) == len(p.Steps) {
		return p.StepOrder
	}
	out := make([]string, 0, len(p.Steps))
	for k := range p.Steps {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SortedVars returns variable names in file order (or sorted).
func (p *Pipeline) SortedVars() []string {
	if len(p.VarOrder) == len(p.Variables) {
		return p.VarOrder
	}
	out := make([]string, 0, len(p.Variables))
	for k := range p.Variables {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Reachable returns the set of nodes reachable from start over edges,
// not expanding past any node in stop.
func Reachable(start string, edges []Edge, stop map[string]bool) map[string]bool {
	adj := map[string][]string{}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if stop[n] {
			continue
		}
		for _, m := range adj[n] {
			if !seen[m] {
				seen[m] = true
				queue = append(queue, m)
			}
		}
	}
	return seen
}
