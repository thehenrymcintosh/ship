package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionsFrozenAndRestored(t *testing.T) {
	in, repo := setup(t)
	s := Open(filepath.Join(repo, ".ship", "history", "p"), t.TempDir())
	v1, _, err := s.Register(Compute(in), SourceEdit, "", nil)
	if err != nil || !v1.Frozen {
		t.Fatalf("%+v %v", v1, err)
	}
	if b, err := os.ReadFile(filepath.Join(v1.Dir, "files", "skill", "ship-implement", "reference.md")); err != nil || string(b) != "ref" {
		t.Fatalf("skill copy: %q %v", b, err)
	}
	// Edit a skill and the rules: v2.
	write(t, filepath.Join(repo, ".claude", "skills", "ship-implement", "reference.md"), "ref v2")
	write(t, filepath.Join(repo, ".ship", "rules", "style.md"), "style v2")
	v2, created, _ := s.Register(Compute(in), SourceEdit, "", nil)
	if !created || v2.Version != 2 || v2.Parent != v1.Hash {
		t.Fatalf("%+v", v2)
	}
	// version.json holds nothing machine-specific.
	b, _ := os.ReadFile(filepath.Join(v1.Dir, "version.json"))
	if strings.Contains(string(b), repo) || strings.Contains(string(b), `"at"`) {
		t.Fatalf("version.json:\n%s", b)
	}
	// Restore v1: the skill and rules go back where they live now.
	steps, skipped := PlanRestore(v1, Compute(in), nil)
	if len(skipped) != 0 {
		t.Fatal(skipped)
	}
	changed := 0
	for _, st := range steps {
		if !st.Same {
			changed++
			if err := st.Apply(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if changed != 2 {
		t.Fatalf("want 2 changes, got %d: %+v", changed, steps)
	}
	if Compute(in).Hash != v1.Hash {
		t.Fatal("restoring v1 should give v1's fingerprint")
	}
	// A removed skill comes back from where it lived.
	os.RemoveAll(filepath.Join(repo, ".claude", "skills", "ship-implement"))
	steps, _ = PlanRestore(v1, Compute(in), nil)
	for _, st := range steps {
		st.Apply()
	}
	if Compute(in).Hash != v1.Hash {
		t.Fatal("removed skill not restored")
	}
	if v, err := s.Find("v2"); err != nil || v.Hash != v2.Hash {
		t.Fatal(v, err)
	}
	if v, err := s.Find(v1.Hash[:6]); err != nil || v.Hash != v1.Hash {
		t.Fatal(v, err)
	}
}

// Two people recording history for the same pipeline on different machines
// write different files (or identical ones), so their commits merge.
func TestHistoryMergesAcrossMachines(t *testing.T) {
	in, repo := setup(t)
	a := Open(filepath.Join(t.TempDir(), "a"), t.TempDir())
	b := Open(filepath.Join(t.TempDir(), "b"), t.TempDir())
	va, _, _ := a.Register(Compute(in), SourceEdit, "", nil)
	a.Add(Feedback{Hash: va.Hash, Text: "from a"})
	write(t, filepath.Join(repo, ".ship", "rules", "style.md"), "style b")
	vb0, _, _ := b.Register(Compute(in), SourceEdit, "", nil)
	write(t, filepath.Join(repo, ".ship", "rules", "style.md"), "style")
	b.Register(Compute(in), SourceEdit, "", nil) // the same version a has
	b.Add(Feedback{Text: "from b"})

	merged := Open(filepath.Join(t.TempDir(), "m"), t.TempDir())
	for _, src := range []*Store{a, b} {
		filepath.Walk(src.Dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(src.Dir, p)
			dest := filepath.Join(merged.Dir, rel)
			data, _ := os.ReadFile(p)
			if old, err := os.ReadFile(dest); err == nil && string(old) != string(data) {
				t.Errorf("both machines wrote %s differently", rel)
			}
			write(t, dest, string(data))
			return nil
		})
	}
	vs, _ := merged.Versions()
	if len(vs) != 2 || vs[0].Hash != va.Hash || vs[1].Hash != vb0.Hash {
		t.Fatalf("%+v", vs)
	}
	items, _ := merged.Items()
	if len(items) != 2 || items[0].Text != "from a" || items[1].ID != 2 || items[0].Version != 1 {
		t.Fatalf("%+v", items)
	}
}

func TestLegacyHistoryMigrates(t *testing.T) {
	in, repo := setup(t)
	dir := filepath.Join(repo, ".ship", "history", "p")
	fp := Compute(in)
	parts, _ := json.Marshal(fp.Parts)
	write(t, filepath.Join(dir, "versions.jsonl"),
		`{"version":1,"hash":"`+fp.Hash+`","at":"2026-01-01T00:00:00Z","source":"first","parts":`+string(parts)+"}\n"+
			`{"version":2,"hash":"bbbbbbbbbbbbbbbbbbbb","at":"2026-01-02T00:00:00Z","source":"refine","summary":"tighter","addresses":[1],"parts":[]}`+"\n")
	write(t, filepath.Join(dir, "feedback.jsonl"),
		`{"type":"feedback","id":1,"at":"2026-01-01T10:00:00Z","version":1,"text":"vague"}`+"\n"+
			`{"type":"feedback","id":2,"at":"2026-01-01T11:00:00Z","version":1,"text":"slow"}`+"\n"+
			`{"type":"close","id":2,"at":"2026-01-01T12:00:00Z","note":"fine"}`+"\n")
	s := Open(dir, t.TempDir())
	vs, err := s.Versions()
	if err != nil || len(vs) != 2 || vs[1].Summary != "tighter" || vs[1].Source != SourceRefine || vs[1].Parent != fp.Hash {
		t.Fatalf("%+v %v", vs, err)
	}
	if !vs[0].Frozen {
		t.Fatal("unchanged parts should have been copied")
	}
	items, _ := s.Items()
	if len(items) != 2 || items[0].Status != "addressed" || items[0].AddressedIn != 2 || items[1].Status != "closed" {
		t.Fatalf("%+v", items)
	}
	// New feedback numbers on.
	f, _, _ := s.Add(Feedback{Text: "new"})
	if f.ID != 3 {
		t.Fatal(f.ID)
	}
	// Converting again changes nothing.
	before, _ := filepath.Glob(filepath.Join(dir, "*", "*"))
	s.Versions()
	after, _ := filepath.Glob(filepath.Join(dir, "*", "*"))
	if len(before) != len(after) {
		t.Fatal("migration isn't idempotent")
	}
}
