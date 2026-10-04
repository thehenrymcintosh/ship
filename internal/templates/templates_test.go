package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBuiltinLibraryLoads(t *testing.T) {
	if _, err := List(Sources{}); err != nil {
		t.Fatal(err)
	}
}

func TestListAddAndShadowing(t *testing.T) {
	builtin := fstest.MapFS{
		"pr/template.yml":           {Data: []byte("description: Built-in PR flow\n")},
		"pr/pipelines/pr.yml":       {Data: []byte("version: 1\n")},
		"basic/template.yml":        {Data: []byte("description: Basic\n")},
		"basic/pipelines/basic.yml": {Data: []byte("version: 1\n")},
	}
	user := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(user, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	write("pr/template.yml", "description: My PR flow\n")
	write("pr/pipelines/pr.yml", "version: 1\n# mine\n")
	write("pr/bin/pr-status", "#!/bin/sh\necho waiting\n")
	write("pr/rules/style.md", "be nice\n")
	write("pr/skills/ship-review/SKILL.md", "---\nname: ship-review\n---\n")
	write("pr/README.md", "not installed\n")

	src := Sources{Builtin: builtin, UserDir: user}
	all, err := List(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Name != "basic" || all[1].Name != "pr" || all[1].Description != "My PR flow" || all[1].Source != user {
		t.Fatalf("%+v %+v", all[0], all[1])
	}
	tpl, err := Get(src, "pr")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tpl.Pipelines(), ","); got != "pr" {
		t.Fatal(got)
	}
	ship, skills := filepath.Join(t.TempDir(), ".ship"), filepath.Join(t.TempDir(), "skills")
	res, err := tpl.Install(ship, skills, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 {
		t.Fatalf("want 4 files, got %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(ship, "pipelines", "pr.yml")); !strings.Contains(string(b), "# mine") {
		t.Fatal("user template should win")
	}
	if st, err := os.Stat(filepath.Join(ship, "bin", "pr-status")); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("bin not executable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skills, "ship-review", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ship, "README.md")); err == nil {
		t.Fatal("README shouldn't be installed")
	}
	// Second install keeps edited files unless forced.
	os.WriteFile(filepath.Join(ship, "pipelines", "pr.yml"), []byte("edited"), 0o644)
	res, _ = tpl.Install(ship, skills, false)
	if res[0].Written || res[1].Written {
		t.Fatalf("should keep existing files: %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(ship, "pipelines", "pr.yml")); string(b) != "edited" {
		t.Fatal("overwrote without force")
	}
	if _, err := Get(src, "nope"); err == nil {
		t.Fatal("want not found")
	}
}
