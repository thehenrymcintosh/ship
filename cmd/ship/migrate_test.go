package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

func TestMigrateToFolder(t *testing.T) {
	pipe := "# yaml-language-server: $schema=../schema/pipeline.json\nversion: 1\nstart: impl\nsteps:\n  impl:\n    agent: /mine\n    next: review\n  review:\n    agent: /shared\n    next: done\n"
	other := "version: 1\nstart: x\nsteps:\n  x:\n    agent: /shared\n    next: done\n"
	h := newHarness(t, map[string]string{"docs": pipe, "other": other})
	for _, s := range []string{"mine", "shared"} {
		os.MkdirAll(filepath.Join(h.repo, ".claude", "skills", s), 0o755)
		os.WriteFile(filepath.Join(h.repo, ".claude", "skills", s, "SKILL.md"), []byte("---\nname: "+s+"\n---\n"), 0o644)
	}
	ship := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(shipBin, append([]string{"--home", h.home}, args...)...)
		cmd.Dir = h.repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ship %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	// Some history first.
	id := h.startRun("docs", "impl: [{outcome: done, summary: ok}]\nreview: [{outcome: done, summary: ok}]\n")
	h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusDone })
	ship("feedback", id, "good")

	out := ship("pipeline", "migrate", "docs", "--skills")
	folder := filepath.Join(h.repo, ".ship", "pipelines", "docs")
	b, err := os.ReadFile(filepath.Join(folder, "pipeline.yml"))
	if err != nil || !strings.Contains(string(b), "$schema=../../schema/") {
		t.Fatalf("%s\n%s", b, out)
	}
	if _, err := os.Stat(filepath.Join(h.repo, ".ship", "pipelines", "docs.yml")); err == nil {
		t.Fatal("old file left behind")
	}
	if _, err := os.Stat(filepath.Join(folder, "skills", "mine", "SKILL.md")); err != nil {
		t.Fatalf("own skill not moved:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(h.repo, ".claude", "skills", "shared", "SKILL.md")); err != nil {
		t.Fatal("a shared skill must stay")
	}
	if _, err := os.Stat(filepath.Join(folder, ".claude-plugin")); err == nil {
		t.Fatal("the folder needs no plugin manifest")
	}
	// The pipeline file was tracked: its move is staged (and the schema
	// path edit isn't). The skill and history weren't: they're just moved.
	st, err := exec.Command("git", "-C", h.repo, "status", "--porcelain").Output()
	if err != nil || !strings.Contains(string(st), "RM .ship/pipelines/docs.yml -> .ship/pipelines/docs/pipeline.yml") {
		t.Fatalf("git status:\n%s", st)
	}
	if _, err := os.Stat(filepath.Join(h.repo, ".ship", "history", "docs")); err == nil {
		t.Fatal("history left behind")
	}
	// The history came along: the feedback is still there.
	if out := ship("pipeline", "feedback", "docs"); !strings.Contains(out, "good") {
		t.Fatal(out)
	}
	if out := ship("validate"); strings.Contains(out, "W108") {
		t.Fatal(out)
	}
}
