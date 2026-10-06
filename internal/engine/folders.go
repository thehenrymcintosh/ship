package engine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// PipelineFolders maps each folder pipeline of a closure to its folder:
// repo-relative when it's inside repo, else absolute. Nil when none is a
// folder.
func PipelineFolders(repo string, closure []*pipeline.File) map[string]string {
	var out map[string]string
	for _, f := range closure {
		folder := pipeline.FolderOf(f.Path)
		if folder == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[f.Name] = folder
		if repo != "" {
			if rel, err := filepath.Rel(repo, folder); err == nil && !strings.HasPrefix(rel, "..") {
				out[f.Name] = filepath.ToSlash(rel)
			}
		}
	}
	return out
}

// pluginDir is where, in a run's dir, a folder pipeline's skills are
// assembled as a plugin for its agents.
const pluginDir = "plugin"

// runFolders resolves a run's pipeline folders as the run sees them: a
// repo folder from root (its worktree), falling back to the main checkout
// when the worktree doesn't have it (e.g. not committed yet).
func runFolders(s *store.RunSnapshot, root string) map[string]string {
	out := map[string]string{}
	for name, f := range s.PipelineFolders {
		if filepath.IsAbs(f) {
			out[name] = f
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(f))
		if _, err := os.Stat(p); err != nil && s.Repo != "" {
			p = filepath.Join(s.Repo, filepath.FromSlash(f))
		}
		out[name] = p
	}
	return out
}

// SkillCheck returns the W108 check for a repo: a skill exists in the
// pipeline's folder, the repo's .claude or the user's ~/.claude.
func (e *Engine) SkillCheck(repo string) func(f *pipeline.File, skill string) bool {
	in := history.Inputs{Repo: repo, ClaudeDir: e.o.ClaudeDir}
	return func(f *pipeline.File, skill string) bool {
		return !in.ResolveSkill(skill, f.Name, pipeline.FolderOf(f.Path)).Missing
	}
}
