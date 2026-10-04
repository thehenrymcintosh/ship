// Package pipeline holds the pipeline file model: types, parsing with
// positions, defaults, validation, schema generation and graph export.
package pipeline

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// Reserved targets.
const (
	TargetDone     = "done"
	TargetStop     = "stop"
	TargetCameFrom = "$came_from"
	OutcomeError   = "error"
)

// Step types.
const (
	TypeAgent  = "agent"
	TypeRun    = "run"
	TypeAsk    = "ask"
	TypeWait   = "wait"
	TypeSplit  = "split"
	TypeFanout = "fanout"
)

// Pipeline is one pipeline file.
type Pipeline struct {
	Version     int                  `yaml:"version" jsonschema:"required,enum=1"`
	Name        string               `yaml:"name,omitempty"`
	Description string               `yaml:"description,omitempty"`
	Start       string               `yaml:"start" jsonschema:"required"`
	Workspace   *Workspace           `yaml:"workspace,omitempty"`
	Agent       *AgentConfig         `yaml:"agent,omitempty"`
	Variables   map[string]*Variable `yaml:"variables,omitempty"`
	Defaults    *Defaults            `yaml:"defaults,omitempty"`
	Limits      *Limits              `yaml:"limits,omitempty"`
	Steps       map[string]*Step     `yaml:"steps" jsonschema:"required"`

	// StepOrder lists step names in file order.
	StepOrder []string `yaml:"-"`
	// VarOrder lists variable names in file order.
	VarOrder []string `yaml:"-"`
}

// Workspace configures where a run works.
type Workspace struct {
	Provider string `yaml:"provider,omitempty" jsonschema:"enum=auto,enum=git,enum=treehouse,enum=none"`
	Branch   string `yaml:"branch,omitempty"`
	Base     string `yaml:"base,omitempty"`
	Reuse    string `yaml:"reuse,omitempty" jsonschema:"enum=parent,enum=none"`
}

// AgentConfig holds agent settings, as a pipeline default or per step.
type AgentConfig struct {
	CLI             string   `yaml:"cli,omitempty"`
	Model           string   `yaml:"model,omitempty"`
	Effort          string   `yaml:"effort,omitempty"`
	PermissionMode  string   `yaml:"permission_mode,omitempty"`
	AllowedTools    []string `yaml:"allowed_tools,omitempty"`
	DisallowedTools []string `yaml:"disallowed_tools,omitempty"`
	ExtraArgs       []string `yaml:"extra_args,omitempty"`
}

// Variable is a pipeline variable. Exactly one source must be set.
type Variable struct {
	Value       *string `yaml:"value,omitempty"`
	FromBrief   *bool   `yaml:"from_brief,omitempty"`
	Ask         *string `yaml:"ask,omitempty"`
	SetBy       *string `yaml:"set_by,omitempty"`
	FromParent  *string `yaml:"from_parent,omitempty"`
	Format      string  `yaml:"format,omitempty"`
	Description string  `yaml:"description,omitempty"`
	Prompt      string  `yaml:"prompt,omitempty"`
}

// Source kinds of a variable.
const (
	SourceValue      = "value"
	SourceFromBrief  = "from_brief"
	SourceAsk        = "ask"
	SourceSetBy      = "set_by"
	SourceFromParent = "from_parent"
)

// Sources lists which source keys are set.
func (v *Variable) Sources() []string {
	var s []string
	if v.Value != nil {
		s = append(s, SourceValue)
	}
	if v.FromBrief != nil && *v.FromBrief {
		s = append(s, SourceFromBrief)
	}
	if v.Ask != nil {
		s = append(s, SourceAsk)
	}
	if v.SetBy != nil {
		s = append(s, SourceSetBy)
	}
	if v.FromParent != nil {
		s = append(s, SourceFromParent)
	}
	return s
}

// Source returns the single source kind, or "" if there isn't exactly one.
func (v *Variable) Source() string {
	if s := v.Sources(); len(s) == 1 {
		return s[0]
	}
	return ""
}

// Defaults apply to every step unless overridden.
type Defaults struct {
	MaxVisits     *int      `yaml:"max_visits,omitempty" jsonschema:"minimum=0"`
	WhenExhausted string    `yaml:"when_exhausted,omitempty"`
	OnError       string    `yaml:"on_error,omitempty"`
	Timeout       *Duration `yaml:"timeout,omitempty"`
	OutputTail    *int      `yaml:"output_tail,omitempty" jsonschema:"minimum=0"`
}

// Limits are per-run hard limits.
type Limits struct {
	MaxTransitions *int     `yaml:"max_transitions,omitempty" jsonschema:"minimum=1"`
	MaxBudgetUSD   *float64 `yaml:"max_budget_usd,omitempty" jsonschema:"minimum=0"`
}

// Step is one named node. Exactly one type key is set.
type Step struct {
	Description   string            `yaml:"description,omitempty"`
	Next          *Next             `yaml:"next,omitempty"`
	MaxVisits     *int              `yaml:"max_visits,omitempty" jsonschema:"minimum=0"`
	WhenExhausted string            `yaml:"when_exhausted,omitempty"`
	OnError       string            `yaml:"on_error,omitempty"`
	Timeout       *Duration         `yaml:"timeout,omitempty"`
	Env           map[string]string `yaml:"env,omitempty"`

	// agent (also split)
	Agent       *string `yaml:"agent,omitempty"`
	Prompt      *string `yaml:"prompt,omitempty"`
	AgentConfig `yaml:",inline"`
	Session     string   `yaml:"session,omitempty" jsonschema:"enum=fresh,enum=continue"`
	Context     []string `yaml:"context,omitempty"`
	Save        *Save    `yaml:"save,omitempty"`
	SaveOn      string   `yaml:"save_on,omitempty" jsonschema:"enum=pass,enum=any"`

	// run
	Run      *string    `yaml:"run,omitempty"`
	Shell    string     `yaml:"shell,omitempty" jsonschema:"enum=bash"`
	Outcomes OrderedMap `yaml:"outcomes,omitempty"`

	// ask
	Ask     *string    `yaml:"ask,omitempty"`
	Show    []string   `yaml:"show,omitempty" jsonschema:"enum=prev.handover,enum=brief,enum=vars,enum=slices"`
	Input   string     `yaml:"input,omitempty" jsonschema:"enum=none,enum=optional,enum=required"`
	Choices OrderedMap `yaml:"choices,omitempty"`

	// wait
	Wait  *string   `yaml:"wait,omitempty"`
	Every *Duration `yaml:"every,omitempty"`

	// split
	Split     *string `yaml:"split,omitempty"`
	Rules     string  `yaml:"rules,omitempty"`
	MaxSlices int     `yaml:"max_slices,omitempty" jsonschema:"minimum=1"`
	Review    bool    `yaml:"review,omitempty"`

	// fanout
	Fanout      *string `yaml:"fanout,omitempty"`
	Mode        string  `yaml:"mode,omitempty" jsonschema:"enum=series,enum=parallel"`
	Stack       bool    `yaml:"stack,omitempty"`
	AdvanceOn   string  `yaml:"advance_on,omitempty"`
	MaxParallel int     `yaml:"max_parallel,omitempty" jsonschema:"minimum=1"`
	OnChildStop string  `yaml:"on_child_stop,omitempty" jsonschema:"enum=halt,enum=continue"`
}

// TypeKeys returns the type keys present on the step. An agent step may set
// `agent`, `prompt` or both; a split step may also carry `prompt`.
func (s *Step) TypeKeys() []string {
	var keys []string
	if s.Run != nil {
		keys = append(keys, TypeRun)
	}
	if s.Ask != nil {
		keys = append(keys, TypeAsk)
	}
	if s.Wait != nil {
		keys = append(keys, TypeWait)
	}
	if s.Split != nil {
		keys = append(keys, TypeSplit)
		if s.Agent != nil {
			keys = append(keys, TypeAgent)
		}
	} else if s.Agent != nil || s.Prompt != nil {
		keys = append(keys, TypeAgent)
	}
	if s.Fanout != nil {
		keys = append(keys, TypeFanout)
	}
	return keys
}

// Type returns the step type, or "" when the step doesn't have exactly one.
func (s *Step) Type() string {
	if k := s.TypeKeys(); len(k) == 1 {
		return k[0]
	}
	return ""
}

// IsAgentLike reports whether the step runs an agent (agent and split).
func (s *Step) IsAgentLike() bool {
	t := s.Type()
	return t == TypeAgent || t == TypeSplit
}

// Str dereferences an optional string.
func Str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// --- Next -----------------------------------------------------------------

// Next is a step's `next`: either one target or a map outcome → target.
type Next struct {
	Target string
	Map    OrderedMap
	IsMap  bool
}

// UnmarshalYAML accepts a string or a mapping.
func (n *Next) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		n.Target = s
		return nil
	}
	var m OrderedMap
	if err := unmarshal(&m); err != nil {
		return err
	}
	n.Map, n.IsMap = m, true
	return nil
}

// MarshalYAML writes the original form.
func (n Next) MarshalYAML() (any, error) {
	if n.IsMap {
		return n.Map.MapSlice(), nil
	}
	return n.Target, nil
}

// --- Save -----------------------------------------------------------------

// Save is an agent step's list of vars, or a run step's map var → source.
type Save struct {
	Names   []string   // agent form
	Sources OrderedMap // run form
	IsMap   bool
}

// UnmarshalYAML accepts a list or a mapping.
func (s *Save) UnmarshalYAML(unmarshal func(any) error) error {
	var l []string
	if err := unmarshal(&l); err == nil {
		s.Names = l
		return nil
	}
	var m OrderedMap
	if err := unmarshal(&m); err != nil {
		return err
	}
	s.Sources, s.IsMap = m, true
	return nil
}

// MarshalYAML writes the original form.
func (s Save) MarshalYAML() (any, error) {
	if s.IsMap {
		return s.Sources.MapSlice(), nil
	}
	return s.Names, nil
}

// VarNames returns the variables the step saves, in either form.
func (s *Save) VarNames() []string {
	if s == nil {
		return nil
	}
	if s.IsMap {
		return s.Sources.Keys()
	}
	return s.Names
}

// --- OrderedMap -------------------------------------------------------------

// KV is one ordered map entry.
type KV struct{ Key, Value string }

// OrderedMap is a string map that keeps file order (choices, next, outcomes).
type OrderedMap []KV

// UnmarshalYAML decodes a mapping of scalars, stringifying keys (so exit code
// keys like `0:` work) and values.
func (m *OrderedMap) UnmarshalYAML(unmarshal func(any) error) error {
	var ms yaml.MapSlice
	if err := unmarshal(&ms); err != nil {
		return err
	}
	out := make(OrderedMap, 0, len(ms))
	for _, it := range ms {
		v := ""
		if it.Value != nil {
			v = fmt.Sprint(it.Value)
		}
		out = append(out, KV{Key: fmt.Sprint(it.Key), Value: v})
	}
	*m = out
	return nil
}

// MapSlice converts back for marshalling.
func (m OrderedMap) MapSlice() yaml.MapSlice {
	ms := make(yaml.MapSlice, len(m))
	for i, kv := range m {
		ms[i] = yaml.MapItem{Key: kv.Key, Value: kv.Value}
	}
	return ms
}

// MarshalYAML keeps order.
func (m OrderedMap) MarshalYAML() (any, error) { return m.MapSlice(), nil }

// Get returns the value for key.
func (m OrderedMap) Get(key string) (string, bool) {
	for _, kv := range m {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return "", false
}

// Keys returns the keys in order.
func (m OrderedMap) Keys() []string {
	out := make([]string, len(m))
	for i, kv := range m {
		out[i] = kv.Key
	}
	return out
}

// --- Duration -------------------------------------------------------------

// Duration is a Go duration string, plus `d` for days.
type Duration time.Duration

var durationRE = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h|d))+$`)
var dayRE = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)d`)

// ParseDuration parses "90s", "5m", "2h", "7d", "1d12h".
func ParseDuration(s string) (time.Duration, error) {
	if !durationRE.MatchString(s) {
		return 0, fmt.Errorf("invalid duration %q (use e.g. 90s, 5m, 2h, 7d)", s)
	}
	var convErr error
	conv := dayRE.ReplaceAllStringFunc(s, func(m string) string {
		f, err := strconv.ParseFloat(strings.TrimSuffix(m, "d"), 64)
		if err != nil {
			convErr = err
		}
		return strconv.FormatFloat(f*24, 'f', -1, 64) + "h"
	})
	if convErr != nil {
		return 0, convErr
	}
	return time.ParseDuration(conv)
}

// UnmarshalYAML parses a duration string.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML writes a Go duration string.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// D returns the time.Duration, or def when d is nil.
func (d *Duration) D(def time.Duration) time.Duration {
	if d == nil {
		return def
	}
	return time.Duration(*d)
}
