package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMerges(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(home, "config.yml"), []byte("max_agents: 5\nagent:\n  model: opus\n  allowed_tools: [a, b]\nui:\n  port: 9000\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, ".ship"), 0o755)
	os.WriteFile(filepath.Join(repo, ".ship", "config.yml"), []byte("agent:\n  effort: high\n  allowed_tools: [c]\nworkspace:\n  provider: none\n"), 0o644)
	c, err := Load(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxAgents != 5 || c.Agent.Model != "opus" || c.Agent.Effort != "high" || c.Agent.CLI != "claude" {
		t.Errorf("agent merge: %+v", c)
	}
	if len(c.Agent.AllowedTools) != 1 || c.Agent.AllowedTools[0] != "c" {
		t.Errorf("lists should replace: %v", c.Agent.AllowedTools)
	}
	if c.UI.Port != 9000 || !c.UI.OpenOnStart || c.Workspace.Provider != "none" || !c.Workspace.Fetch {
		t.Errorf("%+v", c)
	}
}

func TestLoadRejectsUnknown(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.yml"), []byte("colour: blue\n"), 0o644)
	if _, err := Load(home, ""); err == nil {
		t.Fatal("want error for unknown key")
	}
}

func TestSetDefaultPipeline(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	file := RepoFile(repo)
	if err := SetDefaultPipeline(file, "release"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(UserFile(home), []byte("default_pipeline: other\n"), 0o644)
	if c, err := Load(home, repo); err != nil || c.DefaultPipeline != "release" {
		t.Fatalf("%v %+v", err, c)
	}
	// Replacing keeps the rest of the file.
	os.WriteFile(file, []byte("# mine\nmax_agents: 2\ndefault_pipeline: release\n"), 0o644)
	if err := SetDefaultPipeline(file, "fix"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "# mine\nmax_agents: 2\ndefault_pipeline: \"fix\"\n" {
		t.Fatalf("%q", b)
	}
	SetDefaultPipeline(file, "")
	if c, _ := Load(home, repo); c.DefaultPipeline != "other" || c.MaxAgents != 2 {
		t.Fatalf("clearing should fall back to the user's: %+v", c)
	}
}
