package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runShip(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(shipBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ship %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestInitInstallsHandoffSkill(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(root, "repo")
	os.MkdirAll(repo, 0o755)
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	env := []string{"SHIP_HOME=" + filepath.Join(root, "home"), "CLAUDE_CONFIG_DIR=" + filepath.Join(root, "claude")}
	skill := filepath.Join(".claude", "skills", "ship-handoff", "SKILL.md")
	// The repo root and .ship/ are both JetBrains projects; the root already
	// has a jsonSchemas.xml with another mapping that must survive.
	os.MkdirAll(filepath.Join(repo, ".idea"), 0o755)
	os.MkdirAll(filepath.Join(repo, ".ship", ".idea"), 0o755)
	os.WriteFile(filepath.Join(repo, ".idea", "jsonSchemas.xml"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="JsonSchemaMappingsProjectConfiguration">
    <state>
      <map>
        <entry key="other"><value><SchemaInfo><option name="name" value="other" /></SchemaInfo></value></entry>
      </map>
    </state>
  </component>
</project>
`), 0o644)

	out := runShip(t, repo, env, "init")
	b, err := os.ReadFile(filepath.Join(repo, skill))
	if err != nil || !strings.Contains(string(b), "name: ship-handoff") {
		t.Fatalf("repo skill not installed by default: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hand this to ship") {
		t.Errorf("init should mention the handoff:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "skills", "ship-feedback", "SKILL.md")); err != nil {
		t.Fatalf("feedback skill missing: %v", err)
	}
	design, err := os.ReadFile(filepath.Join(repo, ".claude", "skills", "ship-design", "SKILL.md"))
	if err != nil || !strings.Contains(string(design), "disable-model-invocation: true") {
		t.Fatalf("design skill missing or model-invocable: %v", err)
	}
	rootXML, _ := os.ReadFile(filepath.Join(repo, ".idea", "jsonSchemas.xml"))
	for _, want := range []string{`key="other"`, `value=".ship/schema/pipeline.json"`, `value=".ship/pipelines/*.yml"`, `value=".ship/config.yml"`} {
		if !strings.Contains(string(rootXML), want) {
			t.Errorf("root .idea/jsonSchemas.xml missing %s:\n%s", want, rootXML)
		}
	}
	shipXML, _ := os.ReadFile(filepath.Join(repo, ".ship", ".idea", "jsonSchemas.xml"))
	for _, want := range []string{`value="schema/pipeline.json"`, `value="pipelines/*.yml"`, `value="config.yml"`} {
		if !strings.Contains(string(shipXML), want) {
			t.Errorf(".ship/.idea/jsonSchemas.xml missing %s:\n%s", want, shipXML)
		}
	}
	// Re-running doesn't duplicate mappings.
	runShip(t, repo, env, "init")
	again, _ := os.ReadFile(filepath.Join(repo, ".idea", "jsonSchemas.xml"))
	if strings.Count(string(again), `key="ship pipeline"`) != 1 {
		t.Errorf("mapping duplicated:\n%s", again)
	}

	// --no-skill skips it.
	repo2 := filepath.Join(root, "repo2")
	os.MkdirAll(repo2, 0o755)
	exec.Command("git", "-C", repo2, "init", "-q").Run()
	runShip(t, repo2, env, "init", "--no-skill")
	if _, err := os.Stat(filepath.Join(repo2, skill)); err == nil {
		t.Fatal("--no-skill still installed the skill")
	}
	// The old --skill flag is still accepted.
	runShip(t, repo2, env, "init", "--skill")

	// --global installs it for the user.
	runShip(t, root, env, "init", "--global")
	if _, err := os.Stat(filepath.Join(root, "claude", "skills", "ship-handoff", "SKILL.md")); err != nil {
		t.Fatalf("global skill missing: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, "home", "pipelines")); err != nil || !fi.IsDir() {
		t.Fatalf("global pipelines dir missing: %v", err)
	}
	// init adds no pipelines of its own.
	if entries, _ := os.ReadDir(filepath.Join(repo, ".ship", "pipelines")); len(entries) != 0 {
		t.Fatalf("init shouldn't add pipelines, found %d", len(entries))
	}
}

func TestTemplatesAndAdd(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	os.MkdirAll(repo, 0o755)
	exec.Command("git", "-C", repo, "init", "-q").Run()
	env := []string{"SHIP_HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(root, "claude")}

	// The built-in ones are listed before the user has any.
	if out := runShip(t, repo, env, "templates"); !strings.Contains(out, "rigorous") || !strings.Contains(out, "built-in") {
		t.Fatalf("built-in library: %s", out)
	}
	tpl := filepath.Join(home, "templates", "review-loop")
	files := map[string]string{
		"template.yml":                   "description: Implement and review until it passes\n",
		"pipelines/review-loop.yml":      "version: 1\nstart: implement\nsteps:\n  implement:\n    agent: /ship-implement\n    next: check\n  check:\n    run: .ship/bin/check\n    next: {pass: done, fail: implement}\n",
		"bin/check":                      "#!/bin/sh\nexit 0\n",
		"skills/ship-implement/SKILL.md": "---\nname: ship-implement\n---\nImplement the brief.\n",
	}
	for rel, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(tpl, rel)), 0o755)
		os.WriteFile(filepath.Join(tpl, rel), []byte(body), 0o644)
	}
	out := runShip(t, repo, env, "templates")
	if !strings.Contains(out, "review-loop") || !strings.Contains(out, "Implement and review until it passes") {
		t.Fatalf("list: %s", out)
	}
	out = runShip(t, repo, env, "add", "review-loop")
	for _, p := range []string{".ship/pipelines/review-loop.yml", ".ship/bin/check", ".claude/skills/ship-implement/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(repo, p)); err != nil {
			t.Fatalf("%s missing after add:\n%s", p, out)
		}
	}
	if st, _ := os.Stat(filepath.Join(repo, ".ship", "bin", "check")); st.Mode().Perm()&0o100 == 0 {
		t.Fatal("bin/check should be executable")
	}
	if !strings.Contains(out, "ship start review-loop") {
		t.Errorf("add output: %s", out)
	}
	out = runShip(t, repo, env, "add", "rigorous")
	for _, p := range []string{".ship/pipelines/rigorous.yml", ".claude/skills/ship-review/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(repo, p)); err != nil {
			t.Fatalf("%s missing after add rigorous:\n%s", p, out)
		}
	}
	cmd := exec.Command(shipBin, "add", "nope")
	cmd.Dir, cmd.Env = repo, append(os.Environ(), env...)
	if cmd.Run(); cmd.ProcessState.ExitCode() != exitNotFound {
		t.Fatalf("unknown template exit %d", cmd.ProcessState.ExitCode())
	}
}
