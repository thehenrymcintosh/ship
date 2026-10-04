// Package initfiles holds the starter files `ship init` writes.
package initfiles

import (
	"embed"
	"strings"

	"github.com/merlin-digital/ship/internal/brand"
)

//go:embed files
var files embed.FS

// File is one starter file.
type File struct {
	Path    string // relative to the target dir (".ship/…" for a repo, ~/.ship for global)
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

// Repo returns the files for <repo>/.ship (paths are relative to the repo).
func Repo() []File {
	d := brand.Dir + "/"
	return []File{
		{d + "config.yml", read("config.yml"), 0o644},
		{d + "pipelines/feature.yml", read("pipelines/feature.yml"), 0o644},
		{d + "pipelines/slice.yml", read("pipelines/slice.yml"), 0o644},
		{d + "rules/splitting.md", read("rules/splitting.md"), 0o644},
		{d + "bin/pr-status", read("bin/pr-status"), 0o755},
	}
}

// Global returns the files for the user-level dir (paths relative to home).
// Global pipelines run in each repo's worktree, so they reach shared helpers
// through $SHIP_HOME and shared rules by absolute path, not .ship/….
func Global(home string) []File {
	slice := strings.ReplaceAll(string(read("pipelines/slice.yml")),
		"wait: "+brand.Dir+"/bin/pr-status", `wait: "$`+brand.HomeEnv+`/bin/pr-status"`)
	feature := strings.ReplaceAll(string(read("pipelines/feature.yml")),
		"rules: "+brand.Dir+"/rules/splitting.md", "rules: "+home+"/rules/splitting.md")
	return []File{
		{"pipelines/feature.yml", []byte(feature), 0o644},
		{"pipelines/slice.yml", []byte(slice), 0o644},
		{"rules/splitting.md", read("rules/splitting.md"), 0o644},
		{"bin/pr-status", read("bin/pr-status"), 0o755},
	}
}

// Skill returns the handoff skill (relative to the repo).
func Skill() File {
	return File{".claude/skills/" + brand.SkillName + "/SKILL.md", read("skill/SKILL.md"), 0o644}
}
