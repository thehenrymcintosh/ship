// Package tmpl implements ship's placeholder templates: `{{ name.path }}`,
// `{{raw name.path}}` and the literal `{{"{{"}}`. There is no logic and there
// are no filters.
package tmpl

import (
	"fmt"
	"strconv"
	"strings"
)

// Ref is one placeholder found in a template.
type Ref struct {
	Path   string // e.g. "vars.test"
	Raw    bool   // written as {{raw path}}
	Offset int    // byte offset of "{{" in the source
}

type part struct {
	lit string
	ref *Ref
}

// Template is a parsed template.
type Template struct {
	src   string
	parts []part
}

// SyntaxError reports a malformed placeholder.
type SyntaxError struct {
	Offset int
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("template: %s (at byte %d)", e.Msg, e.Offset)
}

// Parse parses s.
func Parse(s string) (*Template, error) {
	t := &Template{src: s}
	rest, off := s, 0
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			if rest != "" {
				t.parts = append(t.parts, part{lit: rest})
			}
			return t, nil
		}
		if i > 0 {
			t.parts = append(t.parts, part{lit: rest[:i]})
		}
		start := off + i
		body := rest[i+2:]
		j := strings.Index(body, "}}")
		// A quoted literal may itself contain "}}", so scan past the string first.
		if trimmed := strings.TrimLeft(body, " \t"); strings.HasPrefix(trimmed, `"`) {
			lead := len(body) - len(trimmed)
			q, err := strconv.QuotedPrefix(trimmed)
			if err != nil {
				return nil, &SyntaxError{start, "unterminated string literal"}
			}
			after := strings.TrimLeft(trimmed[len(q):], " \t")
			if !strings.HasPrefix(after, "}}") {
				return nil, &SyntaxError{start, "expected }} after string literal"}
			}
			lit, _ := strconv.Unquote(q)
			t.parts = append(t.parts, part{lit: lit})
			consumed := 2 + lead + len(q) + (len(trimmed) - len(q) - len(after)) + 2
			rest = rest[i+consumed:]
			off += i + consumed
			continue
		}
		if j < 0 {
			return nil, &SyntaxError{start, "unclosed {{"}
		}
		inner := strings.TrimSpace(body[:j])
		fields := strings.Fields(inner)
		ref := &Ref{Offset: start}
		switch {
		case len(fields) == 1:
			ref.Path = fields[0]
		case len(fields) == 2 && fields[0] == "raw":
			ref.Path, ref.Raw = fields[1], true
		case len(fields) == 0:
			return nil, &SyntaxError{start, "empty placeholder"}
		default:
			return nil, &SyntaxError{start, fmt.Sprintf("unexpected placeholder %q (no logic or filters are supported)", inner)}
		}
		if !validPathSyntax(ref.Path) {
			return nil, &SyntaxError{start, fmt.Sprintf("invalid placeholder name %q", ref.Path)}
		}
		t.parts = append(t.parts, part{ref: ref})
		consumed := 2 + j + 2
		rest = rest[i+consumed:]
		off += i + consumed
	}
}

func validPathSyntax(p string) bool {
	if p == "" {
		return false
	}
	for _, seg := range strings.Split(p, ".") {
		if seg == "" {
			return false
		}
		for _, r := range seg {
			if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				return false
			}
		}
	}
	return true
}

// Refs returns the placeholders in t.
func (t *Template) Refs() []Ref {
	var out []Ref
	for _, p := range t.parts {
		if p.ref != nil {
			out = append(out, *p.ref)
		}
	}
	return out
}

// Refs parses s and returns its placeholders.
func Refs(s string) ([]Ref, error) {
	t, err := Parse(s)
	if err != nil {
		return nil, err
	}
	return t.Refs(), nil
}

// Mode selects how substituted values are written.
type Mode int

const (
	// Plain substitutes values verbatim.
	Plain Mode = iota
	// Shell single-quotes every substituted value unless written {{raw …}}.
	Shell
)

// Render renders t with values from scope.
func (t *Template) Render(scope *Scope, mode Mode) (string, error) {
	var b strings.Builder
	for _, p := range t.parts {
		if p.ref == nil {
			b.WriteString(p.lit)
			continue
		}
		v, err := scope.Lookup(p.ref.Path)
		if err != nil {
			return "", err
		}
		if mode == Shell && !p.ref.Raw {
			v = ShellQuote(v)
		}
		b.WriteString(v)
	}
	return b.String(), nil
}

// Render parses and renders s.
func Render(s string, scope *Scope, mode Mode) (string, error) {
	t, err := Parse(s)
	if err != nil {
		return "", err
	}
	return t.Render(scope, mode)
}

// ShellQuote quotes s as one POSIX shell word.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// UnsetVarError is returned when a set_by variable is referenced before any
// step has set it.
type UnsetVarError struct{ Name string }

func (e *UnsetVarError) Error() string { return "variable " + e.Name + " is not set yet" }

// UnknownError is returned for a placeholder that names nothing.
type UnknownError struct{ Path string }

func (e *UnknownError) Error() string { return "unknown placeholder " + e.Path }
