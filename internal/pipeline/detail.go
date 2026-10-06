package pipeline

import "time"

// StepDetail is what a step does, for the graph's step panel. Only the
// fields that apply to the step's type are set.
type StepDetail struct {
	// agent (also split)
	Agent           string   `json:"agent,omitempty"`
	Prompt          string   `json:"prompt,omitempty"`
	CLI             string   `json:"cli,omitempty"`
	Model           string   `json:"model,omitempty"`
	Effort          string   `json:"effort,omitempty"`
	PermissionMode  string   `json:"permission_mode,omitempty"`
	AllowedTools    []string `json:"allowed_tools,omitempty"`
	DisallowedTools []string `json:"disallowed_tools,omitempty"`
	Session         string   `json:"session,omitempty"`
	Context         []string `json:"context,omitempty"`
	Save            []Pair   `json:"save,omitempty"` // var, and for run steps its source
	SaveOn          string   `json:"save_on,omitempty"`
	MaxBudgetUSD    float64  `json:"max_budget_usd,omitempty"`
	MaxTokens       int64    `json:"max_tokens,omitempty"`

	// run
	Run      string `json:"run,omitempty"`
	Shell    string `json:"shell,omitempty"`
	ExitCode []Pair `json:"exit_codes,omitempty"` // exit code → outcome

	// ask
	Ask   string   `json:"ask,omitempty"`
	Show  []string `json:"show,omitempty"`
	Input string   `json:"input,omitempty"`

	// wait
	Wait  string `json:"wait,omitempty"`
	Every string `json:"every,omitempty"`

	// split
	Split     string `json:"split,omitempty"`
	Rules     string `json:"rules,omitempty"`
	MaxSlices int    `json:"max_slices,omitempty"`
	Review    bool   `json:"review,omitempty"`

	// pr
	PR      string `json:"pr,omitempty"`
	Settle  string `json:"settle,omitempty"`
	Trigger string `json:"trigger,omitempty"`

	// fanout
	Fanout      string `json:"fanout,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Stack       bool   `json:"stack,omitempty"`
	AdvanceOn   string `json:"advance_on,omitempty"`
	MaxParallel int    `json:"max_parallel,omitempty"`
	OnChildStop string `json:"on_child_stop,omitempty"`

	// Routes are where each outcome goes, including error and exhausted.
	Routes []Route `json:"routes,omitempty"`
	// Effective limits and fallbacks (defaults applied).
	MaxVisits     int    `json:"max_visits,omitempty"`
	WhenExhausted string `json:"when_exhausted,omitempty"`
	OnError       string `json:"on_error,omitempty"`
	Timeout       string `json:"timeout,omitempty"`
}

// Pair is one ordered key/value of a step detail.
type Pair struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

// Route is where one outcome of a step leads.
type Route struct {
	Outcome string `json:"outcome"`
	To      string `json:"to"`
	Kind    string `json:"kind"` // outcome, error, exhausted
}

func pairs(m OrderedMap) []Pair {
	var out []Pair
	for _, kv := range m {
		out = append(out, Pair{Key: kv.Key, Value: kv.Value})
	}
	return out
}

func durStr(d *Duration) string {
	if d == nil {
		return ""
	}
	return time.Duration(*d).String()
}

// StepDetail describes the named step, or returns nil if there is none.
func (p *Pipeline) StepDetail(name string) *StepDetail {
	s := p.Steps[name]
	if s == nil {
		return nil
	}
	d := &StepDetail{
		Agent: Str(s.Agent), Prompt: Str(s.Prompt),
		CLI: s.CLI, Model: s.Model, Effort: s.Effort, PermissionMode: s.PermissionMode,
		AllowedTools: s.AllowedTools, DisallowedTools: s.DisallowedTools,
		Session: s.Session, Context: s.Context, SaveOn: s.SaveOn,
		MaxBudgetUSD: deref(s.MaxBudgetUSD), MaxTokens: s.MaxTokens.N(),
		Run: Str(s.Run), Shell: s.Shell, ExitCode: pairs(s.Outcomes),
		Ask: Str(s.Ask), Show: s.Show, Input: s.Input,
		Wait: Str(s.Wait), Every: durStr(s.Every),
		Split: Str(s.Split), Rules: s.Rules, MaxSlices: s.MaxSlices, Review: s.Review,
		PR: Str(s.PR), Settle: durStr(s.Settle), Trigger: s.Trigger,
		Fanout: Str(s.Fanout), Mode: s.Mode, Stack: s.Stack, AdvanceOn: s.AdvanceOn,
		MaxParallel: s.MaxParallel, OnChildStop: s.OnChildStop,
		MaxVisits: p.MaxVisits(s), WhenExhausted: p.WhenExhausted(s), OnError: p.OnError(s),
	}
	if s.Save != nil {
		if s.Save.IsMap {
			d.Save = pairs(s.Save.Sources)
		} else {
			for _, n := range s.Save.Names {
				d.Save = append(d.Save, Pair{Key: n})
			}
		}
	}
	if t := p.Timeout(s); t > 0 {
		d.Timeout = t.String()
	}
	if s.Type() == TypeWait && s.Every == nil {
		d.Every = DefaultEvery.String()
	}
	for _, e := range p.Edges() {
		if e.From == name {
			d.Routes = append(d.Routes, Route{Outcome: e.Label, To: e.To, Kind: e.Kind})
		}
	}
	return d
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}
