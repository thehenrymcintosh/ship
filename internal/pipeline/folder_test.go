package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeT(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

const folderPipe = `version: 1
start: greet
steps:
  greet:
    agent: /greet
    next: hello
  hello:
    agent: /folder:wave
    next: other
  other:
    agent: /elsewhere:thing
    next: done
`

func TestFolderPipelines(t *testing.T) {
	dir := t.TempDir()
	writeT(t, filepath.Join(dir, "folder", "pipeline.yml"), folderPipe)
	writeT(t, filepath.Join(dir, "folder", "skills", "greet", "SKILL.md"), "greet")
	writeT(t, filepath.Join(dir, "single.yml"), "version: 1\nstart: a\nsteps:\n  a: {run: 'true', next: {pass: done, fail: stop}}\n")
	writeT(t, filepath.Join(dir, "notapipeline", "readme.md"), "x")
	// A folder wins over a file of the same name.
	writeT(t, filepath.Join(dir, "both.yml"), "version: 1\nstart: a\nsteps:\n  a: {run: 'true', next: {pass: done, fail: stop}}\n")
	writeT(t, filepath.Join(dir, "both", "pipeline.yml"), "version: 1\nstart: b\nsteps:\n  b: {run: 'true', next: {pass: done, fail: stop}}\n")

	l := NewLoader(dir)
	if got := strings.Join(l.Names(), ","); got != "both,folder,single" {
		t.Fatalf("names: %s", got)
	}
	if l.Folder("folder") != filepath.Join(dir, "folder") || l.Folder("single") != "" || l.Dir("folder") != dir {
		t.Fatalf("folder=%q single=%q dir=%q", l.Folder("folder"), l.Folder("single"), l.Dir("folder"))
	}
	if f, _ := l.Load("both"); f == nil || f.Pipeline.Start != "b" {
		t.Fatal("folder should shadow the file")
	}
	f, findings := l.Load("folder")
	if f == nil || f.Name != "folder" {
		t.Fatalf("%v %v", f, findings)
	}

	// W108: /greet is in the folder; /folder:wave isn't; other plugins aren't checked.
	exists := func(f *File, skill string) bool {
		s := strings.TrimPrefix(skill, f.Name+":")
		return FolderSkill(FolderOf(f.Path), s) != ""
	}
	_, findings = ValidateFile(filepath.Join(dir, "folder"+string(filepath.Separator)+"pipeline.yml"), Options{SkillExists: exists})
	var w108 []string
	for _, fd := range findings {
		if fd.Code == "W108" {
			w108 = append(w108, fd.Message)
		}
	}
	if len(w108) != 1 || !strings.Contains(w108[0], "/folder:wave") || !strings.Contains(w108[0], "the pipeline's skills folder") {
		t.Fatalf("W108: %v (all: %v)", w108, findings)
	}

	// The plugin is built elsewhere, from the folder's skills, and
	// rebuilt from scratch.
	folder := filepath.Join(dir, "folder")
	plugin := filepath.Join(t.TempDir(), "plugin")
	writeT(t, filepath.Join(plugin, "skills", "gone", "SKILL.md"), "stale")
	// Links are followed (to a dir, to a file); a dangling link or a link
	// back up the tree is left out without costing the other skills.
	shared := t.TempDir()
	writeT(t, filepath.Join(shared, "linked", "SKILL.md"), "linked")
	writeT(t, filepath.Join(shared, "file.md"), "file")
	os.Chmod(filepath.Join(shared, "file.md"), 0o600)
	skills := filepath.Join(folder, SkillsDir)
	for link, target := range map[string]string{
		"linked":         filepath.Join(shared, "linked"),
		"greet/ref.md":   filepath.Join(shared, "file.md"),
		"greet/dangling": filepath.Join(shared, "nope"),
		"greet/loop":     skills,
	} {
		if err := os.Symlink(target, filepath.Join(skills, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
	skipped, err := BuildPlugin(plugin, folder, "folder", "Greets")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(skipped, ","); got != "greet/dangling,greet/loop" {
		t.Fatalf("skipped: %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(plugin, "skills", "linked", "SKILL.md")); string(b) != "linked" {
		t.Fatal("linked skill dir not copied")
	}
	if st, err := os.Lstat(filepath.Join(plugin, "skills", "greet", "ref.md")); err != nil || st.Mode() != 0o600 {
		t.Fatalf("linked file: %v %v", st, err)
	}
	var m Manifest
	b, _ := os.ReadFile(filepath.Join(plugin, PluginManifest))
	if json.Unmarshal(b, &m) != nil || m.Name != "folder" || m.Version == "" || m.Description != "Greets" {
		t.Fatalf("%s", b)
	}
	if _, err := os.Stat(filepath.Join(plugin, "skills", "greet", "SKILL.md")); err != nil {
		t.Fatal("skill not copied")
	}
	if _, err := os.Stat(filepath.Join(plugin, "skills", "gone")); err == nil {
		t.Fatal("stale skill kept")
	}
	if _, err := os.Stat(filepath.Join(folder, PluginManifest)); err == nil {
		t.Fatal("wrote into the folder")
	}
}

func TestNameFromPath(t *testing.T) {
	for in, want := range map[string]string{"a/b/pr.yml": "pr", "x/pr.yaml": "pr", "x/pr/pipeline.yml": "pr"} {
		if got := NameFromPath(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
	if SkillName("/review mode=code") != "review" || SkillName("Do it") != "" || SkillName("/{{vars.s}}") != "" || SkillName("/p:s x") != "p:s" {
		t.Fatal("SkillName")
	}
}
