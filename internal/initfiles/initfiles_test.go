package initfiles

import (
	"os"
	"path/filepath"
	"testing"
)

// Run scripts/skill-hashes.sh after changing a skill, or ship won't
// recognise this version as its own and will never update it.
func TestCurrentSkillsAreListedAsShipped(t *testing.T) {
	for _, f := range Skills() {
		if !shipped(f.Content) {
			t.Errorf("%s isn't in shipped.txt: run scripts/skill-hashes.sh", f.Path)
		}
	}
}

func TestSyncUpdatesOnlyUneditedCopies(t *testing.T) {
	dir := t.TempDir()
	if Installed(dir) {
		t.Fatal("empty dir has skills")
	}
	if rs, err := Sync(dir, false); err != nil || rs[0].Action != Wrote {
		t.Fatalf("%v %+v", err, rs)
	}
	sk := Skills()
	old, edited := filepath.Join(dir, sk[0].Path), filepath.Join(dir, sk[1].Path)
	// An older shipped version, and one someone edited.
	older := []byte("an older version\n")
	shippedList += "\n" + hash(older)
	os.WriteFile(old, older, 0o644)
	os.WriteFile(edited, []byte("my own version\n"), 0o644)

	rs, err := Sync(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].Action != Updated || rs[1].Action != Edited || rs[2].Action != Unchanged {
		t.Fatalf("%+v", rs)
	}
	if b, _ := os.ReadFile(edited); string(b) != "my own version\n" {
		t.Fatal("edited skill was overwritten")
	}
	if rs, _ := Sync(dir, true); rs[1].Action != Updated {
		t.Fatalf("force: %+v", rs)
	}
}
