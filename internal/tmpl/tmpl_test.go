package tmpl

import (
	"errors"
	"testing"
)

func scope() *Scope {
	s := NewScope([]string{"test", "pr_url", "title"})
	s.Set("vars.test", "make test")
	s.Set("vars.title", "it's here")
	s.MarkUnset("vars.pr_url")
	s.Set("run.id", "r1")
	return s
}

func TestRenderPlainAndShell(t *testing.T) {
	cases := []struct {
		src  string
		mode Mode
		want string
	}{
		{"id={{run.id}}", Plain, "id=r1"},
		{"id={{ run.id }}", Plain, "id=r1"},
		{"{{vars.test}}", Shell, "'make test'"},
		{"{{raw vars.test}}", Shell, "make test"},
		{"echo {{vars.title}}", Shell, `echo 'it'\''s here'`},
		{`a {{"{{"}} b`, Plain, "a {{ b"},
		{`{{ "}}" }}`, Plain, "}}"},
		{"prev={{prev.step}}.", Plain, "prev=."},
		{"no placeholders", Shell, "no placeholders"},
	}
	for _, c := range cases {
		got, err := Render(c.src, scope(), c.mode)
		if err != nil {
			t.Fatalf("%q: %v", c.src, err)
		}
		if got != c.want {
			t.Errorf("%q: got %q want %q", c.src, got, c.want)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	_, err := Render("{{vars.pr_url}}", scope(), Plain)
	var unset *UnsetVarError
	if !errors.As(err, &unset) || unset.Name != "pr_url" {
		t.Fatalf("want unset pr_url, got %v", err)
	}
	if _, err := Render("{{nope.x}}", scope(), Plain); err == nil {
		t.Fatal("want unknown error")
	}
	if _, err := Render("{{vars.undeclared}}", scope(), Plain); err == nil {
		t.Fatal("want undeclared var error")
	}
	for _, bad := range []string{"{{", "{{ }}", "{{a b c}}", "{{if x}}", `{{"x}}`} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q: want syntax error", bad)
		}
	}
}

func TestCheckPath(t *testing.T) {
	vars := map[string]bool{"a": true}
	good := []string{"vars.a", "brief.title", "run.worktree", "prev.handover", "came_from", "visit.number",
		"slice.count", "parent.run.id", "parent.vars.anything", "parent.brief.path"}
	for _, p := range good {
		if err := CheckPath(p, vars); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	bad := []string{"vars.b", "brief.body", "run", "prev.x", "came_from.x", "visit.seq", "parent.x.y", "x"}
	for _, p := range bad {
		if err := CheckPath(p, vars); err == nil {
			t.Errorf("%s: want error", p)
		}
	}
}

func TestRefs(t *testing.T) {
	refs, err := Refs("x {{raw vars.a}} {{run.id}}")
	if err != nil || len(refs) != 2 || !refs[0].Raw || refs[1].Path != "run.id" || refs[0].Offset != 2 {
		t.Fatalf("got %+v %v", refs, err)
	}
}
