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

	out := runShip(t, repo, env, "init")
	b, err := os.ReadFile(filepath.Join(repo, skill))
	if err != nil || !strings.Contains(string(b), "name: ship-handoff") {
		t.Fatalf("repo skill not installed by default: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hand this to ship") {
		t.Errorf("init should mention the handoff:\n%s", out)
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
	if _, err := os.Stat(filepath.Join(root, "home", "pipelines", "feature.yml")); err != nil {
		t.Fatalf("global pipelines missing: %v", err)
	}
}
