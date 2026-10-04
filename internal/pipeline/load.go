package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type loaded struct {
	file     *File
	findings []Finding
}

// Loader parses pipelines from a search path of directories (the repo's
// .ship/pipelines, then the user's global ~/.ship/pipelines), resolving
// names and fanout references by file basename. Earlier directories shadow
// later ones.
type Loader struct {
	Dirs  []string
	cache map[string]*loaded
}

// NewLoader returns a loader searching dirs in order. Empty entries are
// ignored.
func NewLoader(dirs ...string) *Loader {
	var ds []string
	for _, d := range dirs {
		if d != "" {
			ds = append(ds, d)
		}
	}
	return &Loader{Dirs: ds, cache: map[string]*loaded{}}
}

// Path returns the file path for a pipeline name, or "" if missing.
func (l *Loader) Path(name string) string {
	for _, dir := range l.Dirs {
		for _, ext := range []string{".yml", ".yaml"} {
			p := filepath.Join(dir, name+ext)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// Dir returns the directory a pipeline resolves from, or "".
func (l *Loader) Dir(name string) string {
	if p := l.Path(name); p != "" {
		return filepath.Dir(p)
	}
	return ""
}

// Names lists the pipelines on the search path (each name once).
func (l *Loader) Names() []string {
	seen := map[string]bool{}
	var out []string
	for _, dir := range l.Dirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !(strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml")) {
				continue
			}
			name := NameFromPath(n)
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Load parses a pipeline by name (cached). The file is nil when missing or
// unparseable.
func (l *Loader) Load(name string) (*File, []Finding) {
	if c, ok := l.cache[name]; ok {
		return c.file, c.findings
	}
	path := l.Path(name)
	var c loaded
	if path == "" {
		where := strings.Join(l.Dirs, " or ")
		file := name + ".yml"
		if len(l.Dirs) > 0 {
			file = filepath.Join(l.Dirs[0], file)
		}
		c.findings = []Finding{{File: file, Line: 1, Col: 1, Severity: SevError, Code: "E006", Message: fmt.Sprintf("pipeline %q not found in %s", name, where)}}
	} else {
		c.file, c.findings = ParseFile(path)
	}
	l.cache[name] = &c
	return c.file, c.findings
}

// IsChild reports whether any pipeline on the search path fans out to name.
func (l *Loader) IsChild(name string) bool {
	for _, n := range l.Names() {
		f, _ := l.Load(n)
		if f == nil {
			continue
		}
		for _, s := range f.Pipeline.Steps {
			if s != nil && s.Fanout != nil && *s.Fanout == name {
				return true
			}
		}
	}
	return false
}

// Validate parses and fully validates a pipeline by name.
func (l *Loader) Validate(name string, opts Options) (*File, []Finding) {
	f, findings := l.Load(name)
	if f == nil {
		return nil, findings
	}
	opts.Lookup = l.Load
	if !opts.IsChild {
		opts.IsChild = l.IsChild(name)
	}
	out := append(append([]Finding{}, findings...), Validate(f, opts)...)
	SortFindings(out)
	return f, out
}

// Closure returns name and every pipeline reachable from it through fanout,
// each once, name first.
func (l *Loader) Closure(name string) ([]*File, error) {
	var out []*File
	seen := map[string]bool{}
	var visit func(n string) error
	visit = func(n string) error {
		if seen[n] {
			return nil
		}
		seen[n] = true
		f, findings := l.Load(n)
		if f == nil {
			if len(findings) > 0 {
				return fmt.Errorf("%s", findings[0])
			}
			return fmt.Errorf("pipeline %q not found", n)
		}
		out = append(out, f)
		for _, sn := range f.Pipeline.SortedSteps() {
			if s := f.Pipeline.Steps[sn]; s != nil && s.Fanout != nil {
				if err := visit(*s.Fanout); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return out, visit(name)
}

// ValidateFile validates a pipeline at an arbitrary path, resolving fanout
// targets from the same directory, then from extra (e.g. the global dir).
func ValidateFile(path string, opts Options, extra ...string) (*File, []Finding) {
	l := NewLoader(append([]string{filepath.Dir(path)}, extra...)...)
	name := NameFromPath(path)
	if l.Path(name) != path && l.Path(name) != "" && filepath.Clean(l.Path(name)) != filepath.Clean(path) {
		// Unusual extension or a different file of the same name: parse directly.
		f, findings := ParseFile(path)
		if f == nil {
			return nil, findings
		}
		opts.Lookup = l.Load
		return f, append(findings, Validate(f, opts)...)
	}
	return l.Validate(name, opts)
}
