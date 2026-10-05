// Package initfiles holds the Claude Code skills `ship init` installs.
package initfiles

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/thehenrymcintosh/ship/internal/brand"
)

//go:embed files
var files embed.FS

// File is one file to write.
type File struct {
	Path    string // relative to the target dir
	Content []byte
	Mode    uint32
}

func read(name string) []byte {
	b, err := files.ReadFile("files/" + name)
	if err != nil {
		panic(err)
	}
	return b
}

// Skills returns the Claude Code skills, with paths relative to a skills
// dir (<repo>/.claude/skills or ~/.claude/skills): the handoff skill, the
// user-invoked /ship-design skill for designing pipelines, and the feedback
// skill.
func Skills() []File {
	return []File{
		{brand.SkillName + "/SKILL.md", read("skill/SKILL.md"), 0o644},
		{brand.DesignSkillName + "/SKILL.md", read("skill-design/SKILL.md"), 0o644},
		{brand.FeedbackSkillName + "/SKILL.md", read("skill-feedback/SKILL.md"), 0o644},
	}
}

//go:embed shipped.txt
var shippedList string

// shipped reports whether b is a version of an init skill that ship has
// installed at some point, i.e. an unedited copy that's safe to replace.
func shipped(b []byte) bool {
	h := hash(b)
	for _, line := range strings.Split(shippedList, "\n") {
		if strings.TrimSpace(line) == h {
			return true
		}
	}
	return false
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Actions Sync reports for each skill.
const (
	Wrote     = "wrote"     // wasn't there
	Updated   = "updated"   // replaced an older (or, with force, edited) copy
	Unchanged = "unchanged" // already current
	Edited    = "edited"    // kept: someone changed it
)

// Result is what Sync did with one skill.
type Result struct {
	Path   string
	Action string
}

// Installed reports whether any of the skills is in dir.
func Installed(dir string) bool {
	for _, f := range Skills() {
		if _, err := os.Stat(filepath.Join(dir, f.Path)); err == nil {
			return true
		}
	}
	return false
}

// Sync installs the skills in dir and brings unedited copies up to date.
// A copy that doesn't match any version ship has installed is kept, unless
// force is set.
func Sync(dir string, force bool) ([]Result, error) {
	var out []Result
	for _, f := range Skills() {
		path := filepath.Join(dir, f.Path)
		action := Wrote
		if cur, err := os.ReadFile(path); err == nil {
			switch {
			case bytes.Equal(cur, f.Content):
				action = Unchanged
			case force || shipped(cur):
				action = Updated
			default:
				action = Edited
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return out, err
		}
		if action == Wrote || action == Updated {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return out, err
			}
			if err := os.WriteFile(path, f.Content, os.FileMode(f.Mode)); err != nil {
				return out, err
			}
		}
		out = append(out, Result{Path: path, Action: action})
	}
	return out, nil
}
