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
	// Copies are content-addressed: each file once, under its hash.
	if b, err := os.ReadFile(filepath.Join(s.Dir, "objects", hashBytes([]byte("ref")))); err != nil || string(b) != "ref" {
		t.Fatalf("skill copy: %q %v", b, err)
	}
	objects := func() int {
		es, _ := os.ReadDir(filepath.Join(s.Dir, "objects"))
		return len(es)
	}
	before := objects()
	// Edit a skill file and the rules: v2 stores the two new files and the
	// skill's new listing, and nothing else again.
	write(t, filepath.Join(repo, ".claude", "skills", "ship-implement", "reference.md"), "ref v2")
	write(t, filepath.Join(repo, ".ship", "rules", "style.md"), "style v2")
	v2, created, _ := s.Register(Compute(in), SourceEdit, "", nil)
	if !created || v2.Version != 2 || v2.Parent != v1.Hash || !v2.Frozen {
		t.Fatalf("%+v", v2)
	}
	if got := objects() - before; got != 3 {
		t.Fatalf("v2 added %d objects, want 3", got)
	}
	if _, err := os.Stat(filepath.Join(v1.Dir, "files")); err == nil {
		t.Fatal("versions shouldn't keep their own copies")
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
	// Among a subset (the pipeline page's versions with runs), "v2" is
	// still the version numbered 2, not the second in the list.
	if v, err := FindIn([]Version{v2}, "v2"); err != nil || v.Hash != v2.Hash {
		t.Fatal(v, err)
	}
	if _, err := FindIn([]Version{v2}, "v1"); err == nil {
		t.Fatal("v1 isn't in the subset")
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
	// Reading takes the old files as they are, and writes nothing; the
	// next thing recorded converts them, and reads the same.
	for round := 0; round < 2; round++ {
		vs, err := s.Versions()
		if err != nil || len(vs) != 2 || vs[1].Summary != "tighter" || vs[1].Source != SourceRefine || vs[1].Parent != fp.Hash {
			t.Fatalf("%+v %v", vs, err)
		}
		items, _ := s.Items()
		if len(items) < 2 || items[0].Status != "addressed" || items[0].AddressedIn != 2 || items[1].Status != "closed" {
			t.Fatalf("%+v", items)
		}
		if round > 0 {
			break
		}
		if es, _ := os.ReadDir(dir); len(es) != 2 {
			t.Fatalf("reading wrote files: %v", es)
		}
		// New feedback numbers on.
		if f, _, _ := s.Add(Feedback{Text: "new"}); f.ID != 3 {
			t.Fatal(f.ID)
		}
	}
	if vs, _ := s.Versions(); !vs[0].Frozen {
		t.Fatal("unchanged parts should have been copied when converting")
	}
	// Converting again changes nothing.
	files := func() (n int) {
		for _, sub := range []string{"versions", "objects"} {
			filepath.Walk(filepath.Join(dir, sub), func(_ string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					n++
				}
				return nil
			})
		}
		return n
	}
	before := files()
	if err := s.Close(3, "done"); err != nil {
		t.Fatal(err)
	}
	if after := files(); after != before {
		t.Fatalf("migration isn't idempotent: %d files, then %d", before, after)
	}
}

// Versions recorded by early 0.7 builds keep a copy of each part in their
// own files/ dir: they still restore and diff.
func TestVersionFilesDirStillRead(t *testing.T) {
	in, repo := setup(t)
	s := Open(filepath.Join(repo, ".ship", "history", "p"), t.TempDir())
	v1, _, _ := s.Register(Compute(in), SourceEdit, "", nil)
	// Turn v1 into the old layout: copies under files/, no objects.
	for _, p := range Compute(in).Parts {
		if !p.Missing {
			if err := copyPart(p.Path, filepath.Join(v1.Dir, "files", filepath.FromSlash(CopyRel(p)))); err != nil {
				t.Fatal(err)
			}
		}
	}
	os.RemoveAll(filepath.Join(s.Dir, "objects"))
	v, _ := s.Find("v1")
	if !v.Frozen {
		t.Fatalf("%+v", v)
	}
	write(t, filepath.Join(repo, ".claude", "skills", "ship-implement", "SKILL.md"), "changed")
	write(t, filepath.Join(repo, ".claude", "skills", "ship-implement", "extra.md"), "added")
	steps, skipped := PlanRestore(*v, Compute(in), nil)
	for _, st := range steps {
		if err := st.Apply(); err != nil {
			t.Fatal(err)
		}
	}
	if len(skipped) != 0 || Compute(in).Hash != v1.Hash {
		t.Fatalf("not restored from files/: %v", skipped)
	}
	out := t.TempDir()
	if err := Materialize(*v, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "skill", "ship-implement", "reference.md")); string(b) != "ref" {
		t.Fatalf("materialized %q", b)
	}
}
