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

// The handoff skill was ship-handoff; Sync retires the old copy so Claude
// Code doesn't offer both.
func TestSyncRetiresRenamedSkill(t *testing.T) {
	old, err := os.ReadFile("testdata/ship-handoff.md")
	if err != nil || !shipped(old) {
		t.Fatalf("testdata should be a shipped version: %v", err)
	}
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "ship-handoff", "SKILL.md")
	os.MkdirAll(filepath.Dir(oldPath), 0o755)
	os.WriteFile(oldPath, old, 0o644)
	if !Installed(dir) {
		t.Fatal("an old install counts as installed, so refresh-skills reaches it")
	}
	rs, err := Sync(dir, false)
	if err != nil || len(rs) != 4 || rs[3].Action != Removed {
		t.Fatalf("%v %+v", err, rs)
	}
	if _, err := os.Stat(filepath.Dir(oldPath)); err == nil {
		t.Fatal("unedited old skill dir should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "ship", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	// An edited copy is moved aside, keeping its edits.
	os.MkdirAll(filepath.Dir(oldPath), 0o755)
	os.WriteFile(oldPath, []byte("my handoff\n"), 0o644)
	rs, _ = Sync(dir, false)
	if len(rs) != 4 || rs[3].Action != MovedAside {
		t.Fatalf("%+v", rs)
	}
	if b, _ := os.ReadFile(oldPath + ".old"); string(b) != "my handoff\n" {
		t.Fatal("edits lost")
	}
	if _, err := os.Stat(oldPath); err == nil {
		t.Fatal("edited old skill still loadable")
	}
	if rs, _ := Sync(dir, false); len(rs) != 3 {
		t.Fatalf("nothing left to retire: %+v", rs)
	}
}
