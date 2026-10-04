// Package initfiles holds the Claude Code skills `ship init` installs.
package initfiles

import (
	"embed"

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
// dir (<repo>/.claude/skills or ~/.claude/skills): the handoff skill, and
// the user-invoked /ship-design skill for designing pipelines.
func Skills() []File {
	return []File{
		{brand.SkillName + "/SKILL.md", read("skill/SKILL.md"), 0o644},
		{brand.DesignSkillName + "/SKILL.md", read("skill-design/SKILL.md"), 0o644},
	}
}
