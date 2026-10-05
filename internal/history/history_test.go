package history

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/pipeline"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

const pipe = `version: 1
start: impl
steps:
  impl:
    agent: /ship-implement
    next: review
  review:
    agent: /code-review strict
    rules: .ship/rules/style.md
    next: {pass: test, changes: impl}
  test:
    run: .ship/bin/check && "$SHIP_HOME/bin/shared"
    next: {pass: done, fail: impl}
  extra:
    agent: /plugin-only
    next: done
`

func setup(t *testing.T) (Inputs, string) {
	root := t.TempDir()
	repo, home, claude := filepath.Join(root, "repo"), filepath.Join(root, "home"), filepath.Join(root, "claude")
	write(t, filepath.Join(repo, ".ship", "pipelines", "p.yml"), pipe)
	write(t, filepath.Join(repo, ".claude", "skills", "ship-implement", "SKILL.md"), "implement")
	write(t, filepath.Join(repo, ".claude", "skills", "ship-implement", "reference.md"), "ref")
	write(t, filepath.Join(claude, "commands", "code-review.md"), "review cmd")
	write(t, filepath.Join(repo, ".ship", "rules", "style.md"), "style")
	write(t, filepath.Join(repo, ".ship", "bin", "check"), "#!/bin/sh")
	write(t, filepath.Join(home, "bin", "shared"), "#!/bin/sh")
	f, fs := pipeline.ParseFile(filepath.Join(repo, ".ship", "pipelines", "p.yml"))
	if f == nil {
		t.Fatal(fs)
	}
	return Inputs{Pipelines: []*pipeline.File{f}, Repo: repo, Home: home, ClaudeDir: claude}, repo
}

func TestFingerprintParts(t *testing.T) {
	in, repo := setup(t)
	fp := Compute(in)
	var got []string
	for _, p := range fp.Parts {
		s := p.Kind + ":" + p.Name
		if p.Missing {
			s += "(missing)"
		}
		got = append(got, s)
	}
	want := "command:code-review pipeline:p rules:.ship/rules/style.md script:$SHIP_HOME/bin/shared script:.ship/bin/check skill:plugin-only(missing) skill:ship-implement"
	if strings.Join(got, " ") != want {
		t.Fatalf("parts:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	// Any referenced file changing changes the fingerprint; unrelated files don't.
	write(t, filepath.Join(repo, "README.md"), "unrelated")
	if Compute(in).Hash != fp.Hash {
		t.Fatal("unrelated file changed the hash")
	}
	write(t, filepath.Join(repo, ".claude", "skills", "ship-implement", "reference.md"), "ref v2")
	fp2 := Compute(in)
	if fp2.Hash == fp.Hash {
		t.Fatal("skill change didn't change the hash")
	}
	if c := Changed(fp.Parts, fp2.Parts); strings.Join(c, ";") != "skill ship-implement changed" {
		t.Fatal(c)
	}
}

func TestVersionsAndFeedback(t *testing.T) {
	in, repo := setup(t)
	s := Open(filepath.Join(repo, ".ship", "history", "p"), t.TempDir())
	v1, created, err := s.Register(Compute(in), SourceEdit, "", nil)
	if err != nil || !created || v1.Version != 1 || v1.Source != SourceFirst {
		t.Fatalf("%+v %v %v", v1, created, err)
	}
	if again, created, _ := s.Register(Compute(in), SourceEdit, "", nil); created || again.Version != 1 {
		t.Fatal("same hash should reuse the version")
	}
	a, _, _ := s.Add(Feedback{Version: 1, Hash: v1.Hash, Run: "r1", Step: "review", Text: "too vague", Source: FromCLI})
	b, _, _ := s.Add(Feedback{Version: 1, Text: "docs too long", Source: FromPR, SourceID: "gh:1"})
	if dup, ok, _ := s.Add(Feedback{Version: 1, Text: "docs too long", Source: FromPR, SourceID: "gh:1"}); ok || dup.ID != b.ID {
		t.Fatal("duplicate PR comment should be skipped")
	}
	c, _, _ := s.Add(Feedback{Version: 1, Text: "slow", Source: FromUI})
	if a.ID != 1 || b.ID != 2 || c.ID != 3 {
		t.Fatal(a.ID, b.ID, c.ID)
	}
	if _, _, err := s.Add(Feedback{Text: "  "}); err == nil {
		t.Fatal("empty feedback should fail")
	}
	write(t, filepath.Join(repo, ".ship", "rules", "style.md"), "style v2")
	v2, created, _ := s.Register(Compute(in), SourceRefine, "tighter review rules", []int{1})
	if !created || v2.Version != 2 || strings.Join(v2.Changed, ";") != "rules .ship/rules/style.md changed" {
		t.Fatalf("%+v", v2)
	}
	if err := s.Close(3, "won't fix"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(99, ""); err == nil {
		t.Fatal("closing unknown feedback should fail")
	}
	items, _ := s.Items()
	got := []string{}
	for _, it := range items {
		got = append(got, it.Status)
	}
	if strings.Join(got, ",") != "addressed,open,closed" || items[0].AddressedIn != 2 {
		t.Fatalf("%v %+v", got, items[0])
	}
}

func TestConcurrentAdds(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "h"), t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Add(Feedback{Text: "x"}) }()
	}
	wg.Wait()
	items, _ := s.Items()
	seen := map[int]bool{}
	for _, it := range items {
		seen[it.ID] = true
	}
	if len(items) != 20 || len(seen) != 20 {
		t.Fatalf("got %d items, %d distinct ids", len(items), len(seen))
	}
}
