package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

func TestDiffAndRestore(t *testing.T) {
	pipe := "version: 1\nstart: impl\nsteps:\n  impl:\n    agent: /impl\n    next: done\n"
	h := newHarness(t, map[string]string{"p": pipe})
	skill := filepath.Join(h.repo, ".claude", "skills", "impl", "SKILL.md")
	os.MkdirAll(filepath.Dir(skill), 0o755)
	os.WriteFile(skill, []byte("---\nname: impl\n---\nImplement carefully.\n"), 0o644)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", h.repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git("add", ".")
	git("commit", "-qm", "skill")
	ship := func(args ...string) (string, error) {
		cmd := exec.Command(shipBin, append([]string{"--home", h.home, "--no-color"}, args...)...)
		cmd.Dir = h.repo
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := ship(args...)
		if err != nil {
			t.Fatalf("ship %v: %v\n%s", args, err, out)
		}
		return out
	}
	// Runs record v1, then (after the skill changes) v2.
	run := func() {
		t.Helper()
		id := h.startRun("p", "impl: [{outcome: done, summary: ok}]\n")
		h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusDone })
	}
	run()
	os.WriteFile(skill, []byte("---\nname: impl\n---\nImplement quickly.\n"), 0o644)
	git("commit", "-qam", "quick")
	run()
	if out := must("pipeline", "versions", "p"); !strings.Contains(out, "v2") || !strings.Contains(out, "skill impl changed") {
		t.Fatal(out)
	}
	out := must("pipeline", "diff", "p", "v1", "v2")
	if !strings.Contains(out, "-Implement carefully.") || !strings.Contains(out, "+Implement quickly.") || !strings.Contains(out, "v1/skill/impl/SKILL.md") {
		t.Fatal(out)
	}
	if out := must("pipeline", "diff", "p", "v2"); !strings.Contains(out, "are the same") {
		t.Fatal(out)
	}

	// An uncommitted edit: restore refuses.
	os.WriteFile(skill, []byte("---\nname: impl\n---\nImplement quickly!\n"), 0o644)
	if out, err := ship("pipeline", "restore", "p", "v1"); err == nil || !strings.Contains(out, "uncommitted changes") {
		t.Fatalf("%v\n%s", err, out)
	}
	git("checkout", "--", ".")
	out = must("pipeline", "restore", "p", "v1")
	if !strings.Contains(out, "restored skill impl") || !strings.Contains(out, "back to v1") {
		t.Fatal(out)
	}
	if b, _ := os.ReadFile(skill); !strings.Contains(string(b), "carefully") {
		t.Fatalf("skill not restored: %s", b)
	}
	if out := must("pipeline", "restore", "p", "v1"); !strings.Contains(out, "already matches v1") {
		t.Fatal(out)
	}

	// A skill with a linked dir: diff reads through the link; restore
	// writes through it only with --force (it's outside the repo), and
	// keeps the link.
	shared := t.TempDir()
	os.WriteFile(filepath.Join(shared, "a.md"), []byte("refs one\n"), 0o644)
	refs := filepath.Join(filepath.Dir(skill), "refs")
	if err := os.Symlink(shared, refs); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "refs")
	run() // v4 (v3 is what the refused restore recorded first)
	os.WriteFile(filepath.Join(shared, "a.md"), []byte("refs two\n"), 0o644)
	os.WriteFile(filepath.Join(shared, "b.md"), []byte("new\n"), 0o644)
	if out := must("pipeline", "diff", "p", "v4"); !strings.Contains(out, "-refs one") || !strings.Contains(out, "+refs two") {
		t.Fatal(out)
	}
	if out, err := ship("pipeline", "restore", "p", "v4"); err == nil || !strings.Contains(out, "a.md (outside the repo)") {
		t.Fatalf("%v\n%s", err, out)
	}
	must("pipeline", "restore", "p", "v4", "--force")
	if st, err := os.Lstat(refs); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link not kept: %v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(shared, "a.md")); string(b) != "refs one\n" {
		t.Fatalf("linked file not restored: %s", b)
	}
	if _, err := os.Stat(filepath.Join(shared, "b.md")); err == nil {
		t.Fatal("file added since v4 kept")
	}
}
