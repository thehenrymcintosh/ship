package pipeline

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/merlin-digital/ship/internal/tmpl"
)

// Severity of a finding.
type Severity string

// Severities.
const (
	SevError Severity = "error"
	SevWarn  Severity = "warn"
)

// Finding is one validation result.
type Finding struct {
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Col      int      `json:"col"`
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s %s: %s", f.File, f.Line, f.Col, f.Severity, f.Code, f.Message)
}

// HasErrors reports whether any finding is an error.
func HasErrors(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == SevError {
			return true
		}
	}
	return false
}

// SortFindings orders findings by file, line, column and code.
func SortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Col != b.Col {
			return a.Col < b.Col
		}
		return a.Code < b.Code
	})
}

// Options feed context into Validate.
type Options struct {
	// Lookup resolves a pipeline referenced by fanout. nil skips E006.
	Lookup func(name string) (*File, []Finding)
	// IsChild marks a pipeline used as a fanout target (allows from_parent).
	IsChild bool
	// AgentCheck validates an agent configuration for E014 and returns
	// messages; nil accepts anything.
	AgentCheck func(cfg AgentConfig) []string
	// AgentBase is the agent config from user/project config, merged under
	// the pipeline's agent block.
	AgentBase AgentConfig
}

type validator struct {
	f    *File
	p    *Pipeline
	opts Options
	out  []Finding
}

func (v *validator) add(sev Severity, code, ptr, format string, args ...any) {
	pos := v.f.Pos(ptr)
	v.out = append(v.out, Finding{File: v.f.Path, Line: pos.Line, Col: pos.Col, Severity: sev, Code: code, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) errf(code, ptr, format string, args ...any) {
	v.add(SevError, code, ptr, format, args...)
}

func (v *validator) warnf(code, ptr, format string, args ...any) {
	v.add(SevWarn, code, ptr, format, args...)
}

// fieldsByType lists the step fields valid for each type, beyond the common ones.
var fieldsByType = map[string][]string{
	TypeAgent:  {"agent", "prompt", "cli", "model", "effort", "permission_mode", "allowed_tools", "disallowed_tools", "extra_args", "session", "context", "save"},
	TypeRun:    {"run", "shell", "save", "save_on", "outcomes"},
	TypeAsk:    {"ask", "show", "input", "choices"},
	TypeWait:   {"wait", "every"},
	TypeSplit:  {"split", "prompt", "cli", "model", "effort", "permission_mode", "allowed_tools", "disallowed_tools", "extra_args", "session", "context", "rules", "max_slices", "review"},
	TypeFanout: {"fanout", "mode", "stack", "advance_on", "max_parallel", "on_child_stop"},
}

var commonFields = []string{"description", "next", "max_visits", "when_exhausted", "on_error", "timeout", "env"}

// Validate runs the semantic checks (E003–E014, W101–W106) on a parsed file.
func Validate(f *File, opts Options) []Finding {
	v := &validator{f: f, p: f.Pipeline, opts: opts}
	v.run()
	SortFindings(v.out)
	return v.out
}

func (v *validator) run() {
	p := v.p
	stepPtr := func(name string, field ...string) string { return Ptr(append([]string{"steps", name}, field...)...) }

	// E005 reserved names; E003 type keys; E002 fields for the type.
	for _, name := range p.SortedSteps() {
		s := p.Steps[name]
		if name == TargetDone || name == TargetStop || strings.HasPrefix(name, "$") {
			v.errf("E005", stepPtr(name), "step name %q is reserved", name)
		}
		if s == nil {
			v.errf("E003", stepPtr(name), "step %q is empty: it needs one of agent/prompt, run, ask, wait, split or fanout", name)
			continue
		}
		keys := s.TypeKeys()
		if len(keys) != 1 {
			found := "none"
			if len(keys) > 0 {
				found = strings.Join(keys, ", ")
			}
			v.errf("E003", stepPtr(name), "step %q must have exactly one type key (agent/prompt, run, ask, wait, split, fanout); found: %s", name, found)
			continue
		}
		typ := keys[0]
		allowed := map[string]bool{}
		for _, k := range append(append([]string{}, commonFields...), fieldsByType[typ]...) {
			allowed[k] = true
		}
		if typ == TypeAsk {
			delete(allowed, "next")
			delete(allowed, "timeout")
		}
		if typ == TypeFanout {
			delete(allowed, "timeout")
		}
		for _, k := range v.presentKeys(stepPtr(name)) {
			if !allowed[k] {
				hint := ""
				if typ == TypeAsk && k == "next" {
					hint = " (ask steps route with choices)"
				}
				v.errf("E002", stepPtr(name, k), "field %q is not valid on %s steps%s", k, typ, hint)
			}
		}
	}

	// Start and targets (E004).
	if _, ok := p.Steps[p.Start]; !ok {
		v.errf("E004", Ptr("start"), "start names unknown step %q", p.Start)
	}
	checkTarget := func(ptr, t string) {
		if t == "" {
			return
		}
		if t == TargetDone || t == TargetStop || t == TargetCameFrom {
			return
		}
		if _, ok := p.Steps[t]; !ok {
			v.errf("E004", ptr, "unknown target %q (expected a step name, done, stop or $came_from)", t)
		}
	}
	if p.Defaults != nil {
		checkTarget(Ptr("defaults", "when_exhausted"), p.Defaults.WhenExhausted)
		checkTarget(Ptr("defaults", "on_error"), p.Defaults.OnError)
	}
	for _, name := range p.SortedSteps() {
		s := p.Steps[name]
		if s == nil || s.Type() == "" {
			continue
		}
		if s.Next != nil {
			if s.Next.IsMap {
				for _, kv := range s.Next.Map {
					checkTarget(stepPtr(name, "next", kv.Key), kv.Value)
				}
			} else {
				checkTarget(stepPtr(name, "next"), s.Next.Target)
			}
		}
		for _, kv := range s.Choices {
			checkTarget(stepPtr(name, "choices", kv.Key), kv.Value)
		}
		checkTarget(stepPtr(name, "when_exhausted"), s.WhenExhausted)
		checkTarget(stepPtr(name, "on_error"), s.OnError)
	}

	v.checkStepShapes()
	v.checkVariables()
	v.checkTemplates()
	v.checkFanout()
	v.checkGraph()
	v.checkAgents()
}

// presentKeys returns the keys written under a mapping pointer, in file order.
func (v *validator) presentKeys(ptr string) []string {
	type kp struct {
		key string
		pos Pos
	}
	var keys []kp
	prefix := ptr + "/"
	for k, pos := range v.f.pos {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := k[len(prefix):]
		if strings.Contains(rest, "/") {
			continue
		}
		keys = append(keys, kp{strings.ReplaceAll(strings.ReplaceAll(rest, "~1", "/"), "~0", "~"), pos})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].pos.Line != keys[j].pos.Line {
			return keys[i].pos.Line < keys[j].pos.Line
		}
		return keys[i].pos.Col < keys[j].pos.Col
	})
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.key
	}
	return out
}

// checkStepShapes covers E012 and per-type value rules.
func (v *validator) checkStepShapes() {
	p := v.p
	for _, name := range p.SortedSteps() {
		s := p.Steps[name]
		if s == nil {
			continue
		}
		ptr := Ptr("steps", name)
		switch s.Type() {
		case TypeSplit:
			if s.Next == nil || !s.Next.IsMap {
				v.errf("E012", ptr, "split step %q needs a next map with an ok outcome", name)
			} else if _, ok := s.Next.Map.Get("ok"); !ok {
				v.errf("E012", Ptr("steps", name, "next"), "split step %q has no ok outcome", name)
			}
		case TypeFanout:
			if s.Next == nil || !s.Next.IsMap {
				v.errf("E012", ptr, "fanout step %q needs a next map with done and failed", name)
			} else {
				for _, o := range []string{"done", "failed"} {
					if _, ok := s.Next.Map.Get(o); !ok {
						v.errf("E012", Ptr("steps", name, "next"), "fanout step %q has no %s outcome", name, o)
					}
				}
			}
			if s.Stack && s.ModeOrDefault() != "series" {
				v.errf("E002", Ptr("steps", name, "stack"), "stack only applies to mode: series")
			}
		case TypeAsk:
			if len(s.Choices) == 0 {
				v.errf("E012", ptr, "ask step %q has no choices", name)
			}
		case TypeWait:
			if s.Next == nil || !s.Next.IsMap {
				v.errf("E012", ptr, "wait step %q needs a next map of the outcomes its command can print", name)
			}
		case TypeRun:
			for _, kv := range s.Outcomes {
				if kv.Key == "default" {
					continue
				}
				if _, err := strconv.Atoi(kv.Key); err != nil {
					v.errf("E002", Ptr("steps", name, "outcomes", kv.Key), "outcomes keys must be exit codes or default, got %q", kv.Key)
				}
			}
			if s.Save != nil && !s.Save.IsMap && len(s.Save.Names) > 0 {
				v.errf("E002", Ptr("steps", name, "save"), "run steps save with a map var → source (e.g. pr_url: last_line)")
			}
		case TypeAgent:
			if s.Save != nil && s.Save.IsMap {
				v.errf("E002", Ptr("steps", name, "save"), "agent steps save with a list of variable names")
			}
		}
	}
}

// checkVariables covers E008, E010, E013.
func (v *validator) checkVariables() {
	p := v.p
	for _, name := range p.SortedVars() {
		vr := p.Variables[name]
		ptr := Ptr("variables", name)
		if vr == nil {
			v.errf("E013", ptr, "variable %q has no source (value, from_brief, ask, set_by or from_parent)", name)
			continue
		}
		srcs := vr.Sources()
		switch {
		case len(srcs) == 0:
			v.errf("E013", ptr, "variable %q has no source (value, from_brief, ask, set_by or from_parent)", name)
		case len(srcs) > 1:
			v.errf("E013", ptr, "variable %q has several sources: %s", name, strings.Join(srcs, ", "))
		}
		if vr.FromParent != nil && !v.opts.IsChild {
			v.errf("E013", Ptr("variables", name, "from_parent"), "from_parent is only valid in pipelines used by a fanout step")
		}
		if vr.Format != "" {
			if _, err := regexp.Compile(vr.Format); err != nil {
				v.errf("E010", Ptr("variables", name, "format"), "format of %q doesn't compile: %v", name, err)
			}
		}
		if vr.SetBy != nil {
			s, ok := p.Steps[*vr.SetBy]
			switch {
			case !ok:
				v.errf("E008", Ptr("variables", name, "set_by"), "set_by names unknown step %q", *vr.SetBy)
			case !contains(s.Save.VarNames(), name):
				v.errf("E008", Ptr("variables", name, "set_by"), "step %q doesn't save %q (add it to the step's save)", *vr.SetBy, name)
			}
		}
	}
	for _, sname := range p.SortedSteps() {
		s := p.Steps[sname]
		if s == nil || s.Save == nil {
			continue
		}
		for _, vn := range s.Save.VarNames() {
			ptr := Ptr("steps", sname, "save", vn)
			if !s.Save.IsMap {
				ptr = Ptr("steps", sname, "save")
			}
			vr, ok := p.Variables[vn]
			switch {
			case !ok:
				v.errf("E008", ptr, "step %q saves undeclared variable %q", sname, vn)
			case vr == nil || vr.SetBy == nil || *vr.SetBy != sname:
				v.errf("E008", ptr, "step %q saves %q, but the variable isn't declared with set_by: %s", sname, vn, sname)
			}
		}
	}
}

// templateField is one templated string and where it lives.
type templateField struct {
	step   string // "" for non-step fields
	ptr    string
	src    string
	script bool
}

func (v *validator) templateFields() []templateField {
	p := v.p
	var out []templateField
	addf := func(step, ptr string, s *string, script bool) {
		if s != nil && *s != "" {
			out = append(out, templateField{step, ptr, *s, script})
		}
	}
	if p.Workspace != nil {
		addf("", Ptr("workspace", "branch"), &p.Workspace.Branch, false)
		addf("", Ptr("workspace", "base"), &p.Workspace.Base, false)
	}
	for _, name := range p.SortedVars() {
		if vr := p.Variables[name]; vr != nil {
			addf("", Ptr("variables", name, "value"), vr.Value, false)
			addf("", Ptr("variables", name, "ask"), vr.Ask, false)
		}
	}
	for _, name := range p.SortedSteps() {
		s := p.Steps[name]
		if s == nil {
			continue
		}
		sp := func(f ...string) string { return Ptr(append([]string{"steps", name}, f...)...) }
		addf(name, sp("description"), &s.Description, false)
		addf(name, sp("agent"), s.Agent, false)
		addf(name, sp("prompt"), s.Prompt, false)
		addf(name, sp("split"), s.Split, false)
		addf(name, sp("ask"), s.Ask, false)
		addf(name, sp("run"), s.Run, true)
		addf(name, sp("wait"), s.Wait, true)
		for k, val := range s.Env {
			val := val
			addf(name, sp("env", k), &val, false)
		}
	}
	return out
}

// checkTemplates covers E009 and W105.
func (v *validator) checkTemplates() {
	declared := map[string]bool{}
	for name := range v.p.Variables {
		declared[name] = true
	}
	for _, tf := range v.templateFields() {
		refs, err := tmpl.Refs(tf.src)
		if err != nil {
			v.errf("E009", tf.ptr, "%v", err)
			continue
		}
		for _, r := range refs {
			if r.Path == "children" {
				v.errf("E009", tf.ptr, "{{children}} is internal and can't be used in pipelines")
				continue
			}
			if err := tmpl.CheckPath(r.Path, declared); err != nil {
				v.errf("E009", tf.ptr, "%v", err)
				continue
			}
			if tf.script && !r.Raw && strings.HasPrefix(r.Path, "vars.") {
				name := strings.TrimPrefix(r.Path, "vars.")
				if vr := v.p.Variables[name]; vr != nil && vr.Value != nil && strings.ContainsAny(*vr.Value, " \t") {
					v.warnf("W105", tf.ptr, "{{%s}} is substituted as one quoted word but its value %q contains spaces; write {{raw %s}} to run it as a command", r.Path, *vr.Value, r.Path)
				}
			}
		}
	}
}

// varRefs returns the vars.* names a step's templates reference.
func (v *validator) varRefs() map[string][]string {
	out := map[string][]string{}
	for _, tf := range v.templateFields() {
		if tf.step == "" {
			continue
		}
		refs, _ := tmpl.Refs(tf.src)
		for _, r := range refs {
			if strings.HasPrefix(r.Path, "vars.") {
				out[tf.step] = append(out[tf.step], strings.TrimPrefix(r.Path, "vars."))
			}
		}
	}
	return out
}

// checkFanout covers E006 (and advance_on targets).
func (v *validator) checkFanout() {
	if v.opts.Lookup == nil {
		return
	}
	for _, name := range v.p.SortedSteps() {
		s := v.p.Steps[name]
		if s == nil || s.Fanout == nil {
			continue
		}
		ptr := Ptr("steps", name, "fanout")
		child, findings := v.opts.Lookup(*s.Fanout)
		if child == nil {
			msg := fmt.Sprintf("fanout names missing pipeline %q", *s.Fanout)
			if len(findings) > 0 && findings[0].Code != "E006" {
				msg = fmt.Sprintf("fanout pipeline %q doesn't parse: %s", *s.Fanout, findings[0].Message)
			}
			v.errf("E006", ptr, "%s", msg)
			continue
		}
		if cycle := v.fanoutCycle(*s.Fanout, []string{v.f.Name}); cycle != nil {
			v.errf("E006", ptr, "pipelines include each other in a cycle: %s", strings.Join(cycle, " → "))
		}
		if s.AdvanceOn != "" && s.AdvanceOn != TargetDone {
			if _, ok := child.Pipeline.Steps[s.AdvanceOn]; !ok {
				v.errf("E004", Ptr("steps", name, "advance_on"), "advance_on names %q, which isn't a step of pipeline %q", s.AdvanceOn, *s.Fanout)
			}
		}
	}
}

func (v *validator) fanoutCycle(name string, path []string) []string {
	for _, p := range path {
		if p == name {
			return append(path, name)
		}
	}
	f, _ := v.opts.Lookup(name)
	if f == nil {
		return nil
	}
	path = append(path, name)
	for _, sn := range f.Pipeline.SortedSteps() {
		s := f.Pipeline.Steps[sn]
		if s != nil && s.Fanout != nil {
			if c := v.fanoutCycle(*s.Fanout, append([]string{}, path...)); c != nil {
				return c
			}
		}
	}
	return nil
}

// checkGraph covers E007, E011, W101, W102, W103, W106.
func (v *validator) checkGraph() {
	p := v.p
	if _, ok := p.Steps[p.Start]; !ok {
		return
	}
	for _, s := range p.Steps {
		if s == nil || s.Type() == "" {
			return // structural errors already reported
		}
	}
	static := p.Edges()
	edges := ExpandCameFrom(static)

	// E011: $came_from in a step nothing routes into.
	incoming := map[string]bool{}
	for _, e := range static {
		if e.To != TargetCameFrom {
			incoming[e.To] = true
		}
	}
	for _, e := range static {
		if e.To == TargetCameFrom && !incoming[e.From] {
			ptr := Ptr("steps", e.From)
			v.errf("E011", ptr, "step %q routes to $came_from, but no step routes into it", e.From)
		}
	}

	reach := Reachable(p.Start, edges, nil)
	for _, name := range p.SortedSteps() {
		if !reach[name] {
			v.warnf("W101", Ptr("steps", name), "step %q is unreachable from start", name)
		}
	}
	if !reach[TargetDone] {
		v.warnf("W102", Ptr("start"), "no path from start reaches done")
	}

	// E007: fanout reachable without passing a split.
	splits := map[string]bool{}
	for name, s := range p.Steps {
		if s.Type() == TypeSplit {
			splits[name] = true
		}
	}
	noSplit := Reachable(p.Start, edges, splits)
	for _, name := range p.SortedSteps() {
		if p.Steps[name].Type() == TypeFanout && noSplit[name] {
			v.errf("E007", Ptr("steps", name), "fanout step %q can be reached without passing a split step", name)
		}
	}

	// W103: set_by vars possibly used before set. $came_from only returns to
	// steps already visited, so it can't skip a setter; leave those edges out.
	var forward []Edge
	for _, e := range static {
		if e.To != TargetCameFrom {
			forward = append(forward, e)
		}
	}
	refs := v.varRefs()
	for _, user := range p.SortedSteps() {
		for _, vn := range uniq(refs[user]) {
			vr := p.Variables[vn]
			if vr == nil || vr.SetBy == nil {
				continue
			}
			setter := *vr.SetBy
			before := Reachable(p.Start, forward, map[string]bool{setter: true})
			if before[user] {
				v.warnf("W103", Ptr("steps", user), "step %q uses vars.%s, which may not be set yet (it's set by %q, and a path reaches %q without passing it)", user, vn, setter, user)
			}
		}
	}

	// W106: agent steps with no route to a human.
	for _, name := range p.SortedSteps() {
		s := p.Steps[name]
		if s.Type() != TypeAgent {
			continue
		}
		if p.OnError(s) != "" {
			continue
		}
		human := false
		for _, o := range s.OutcomeNames() {
			if t, ok := s.Target(o); ok {
				if ts, ok := p.Steps[t]; ok && ts.Type() == TypeAsk {
					human = true
				}
			}
		}
		if !human {
			v.warnf("W106", Ptr("steps", name), "agent step %q has no outcome leading to a human (ask) and no on_error; an error will park the run", name)
		}
	}
}

// checkAgents covers E014 and W104.
func (v *validator) checkAgents() {
	p := v.p
	if p.Agent != nil {
		if p.Agent.PermissionMode == "bypassPermissions" {
			v.warnf("W104", Ptr("agent", "permission_mode"), "permission_mode: bypassPermissions lets agents run anything without asking")
		}
	}
	for _, name := range p.SortedSteps() {
		s := p.Steps[name]
		if s == nil || !s.IsAgentLike() {
			continue
		}
		cfg := p.AgentFor(v.opts.AgentBase, s)
		if s.PermissionMode == "bypassPermissions" {
			v.warnf("W104", Ptr("steps", name, "permission_mode"), "permission_mode: bypassPermissions lets the agent run anything without asking")
		}
		if v.opts.AgentCheck != nil {
			for _, msg := range v.opts.AgentCheck(cfg) {
				v.errf("E014", Ptr("steps", name), "%s", msg)
			}
		}
	}
}

func contains(s []string, x string) bool {
	for _, y := range s {
		if y == x {
			return true
		}
	}
	return false
}
