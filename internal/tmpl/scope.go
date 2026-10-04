package tmpl

import (
	"fmt"
	"strings"

	"github.com/thehenrymcintosh/ship/internal/brand"
)

// Scope holds the values placeholders resolve to, keyed by full path
// ("vars.test", "run.id", "parent.vars.ticket").
type Scope struct {
	values map[string]string
	unset  map[string]bool
	vars   map[string]bool // declared variable names, for path checks
}

// NewScope returns an empty scope. declared lists the run's variable names.
func NewScope(declared []string) *Scope {
	s := &Scope{values: map[string]string{}, unset: map[string]bool{}, vars: map[string]bool{}}
	for _, v := range declared {
		s.vars[v] = true
	}
	return s
}

// Set sets path to v.
func (s *Scope) Set(path, v string) {
	s.values[path] = v
	delete(s.unset, path)
}

// MarkUnset records that path is a known variable without a value yet.
func (s *Scope) MarkUnset(path string) { s.unset[path] = true }

// Value returns the raw value of path, if set.
func (s *Scope) Value(path string) (string, bool) {
	v, ok := s.values[path]
	return v, ok
}

// Lookup resolves one placeholder path.
func (s *Scope) Lookup(path string) (string, error) {
	if v, ok := s.values[path]; ok {
		return v, nil
	}
	if s.unset[path] {
		return "", &UnsetVarError{Name: strings.TrimPrefix(strings.TrimPrefix(path, "parent."), "vars.")}
	}
	if err := CheckPath(path, s.vars); err != nil {
		return "", err
	}
	// A known placeholder that doesn't apply here (prev.* on a first visit,
	// slice.* outside a child run) renders empty.
	return "", nil
}

var (
	briefKeys = set("path", "title", "acceptance")
	runKeys   = set("id", "dir", "worktree", "branch", "base", "pipeline", "repo")
	prevKeys  = set("step", "outcome", "summary", "handover")
	sliceKeys = set("key", "number", "title", "count")
)

func set(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// CheckPath reports whether path names a known placeholder. vars is
// the set of declared variable names; nil skips the vars.<name> check.
// parent.vars.* is never checked, since the parent pipeline isn't known here.
func CheckPath(path string, vars map[string]bool) error {
	seg := strings.Split(path, ".")
	bad := func() error { return &UnknownError{Path: path} }
	switch seg[0] {
	case "vars":
		if len(seg) != 2 {
			return bad()
		}
		if vars != nil && !vars[seg[1]] {
			return fmt.Errorf("unknown variable %q (declare it under variables:)", seg[1])
		}
	case "brief":
		if len(seg) != 2 || !briefKeys[seg[1]] {
			return bad()
		}
	case "run":
		if len(seg) != 2 || !runKeys[seg[1]] {
			return bad()
		}
	case "prev":
		if len(seg) != 2 || !prevKeys[seg[1]] {
			return bad()
		}
	case "came_from", "children":
		if len(seg) != 1 {
			return bad()
		}
	case "visit":
		if len(seg) != 2 || seg[1] != "number" {
			return bad()
		}
	case "slice":
		if len(seg) != 2 || !sliceKeys[seg[1]] {
			return bad()
		}
	case "parent":
		if len(seg) != 3 {
			return bad()
		}
		switch seg[1] {
		case "run":
			if !runKeys[seg[2]] {
				return bad()
			}
		case "brief":
			if !briefKeys[seg[2]] {
				return bad()
			}
		case "vars":
		default:
			return bad()
		}
	default:
		return bad()
	}
	return nil
}

// EnvName converts a variable name to its env form: "pr-url" → "PR_URL".
func EnvName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// Env returns the SHIP_* environment for the scope.
func (s *Scope) Env() []string {
	p := brand.EnvPrefix
	pairs := []struct{ env, path string }{
		{"RUN_ID", "run.id"}, {"RUN_DIR", "run.dir"}, {"WORKTREE", "run.worktree"},
		{"BRANCH", "run.branch"}, {"BASE", "run.base"}, {"STEP", StepKey}, {"VISIT_SEQ", SeqKey},
		{"PREV_OUTCOME", "prev.outcome"}, {"PREV_HANDOVER", "prev.handover"}, {"BRIEF", "brief.path"},
	}
	var env []string
	for _, kv := range pairs {
		env = append(env, p+kv.env+"="+s.values[kv.path])
	}
	for k, v := range s.values {
		switch {
		case strings.HasPrefix(k, "vars."):
			env = append(env, p+"VAR_"+EnvName(strings.TrimPrefix(k, "vars."))+"="+v)
		case strings.HasPrefix(k, "parent.vars."):
			env = append(env, p+"PARENT_VAR_"+EnvName(strings.TrimPrefix(k, "parent.vars."))+"="+v)
		}
	}
	return env
}

// Internal scope keys that feed the env but can't be written as placeholders.
const (
	StepKey = "@step"
	SeqKey  = "@seq"
)
