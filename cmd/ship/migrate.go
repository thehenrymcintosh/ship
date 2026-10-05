package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
)

// cmdRepo is the --repo flag's repo, or the current dir's ("" outside one).
func cmdRepo(cmd *cobra.Command) (string, error) {
	repoFlag, _ := cmd.Flags().GetString("repo")
	if repoFlag == "" && currentRepo() == "" {
		return "", nil
	}
	return repoRoot(repoFlag)
}

func (a *app) migrateCmd() *cobra.Command {
	var skills bool
	cmd := &cobra.Command{
		Use:   "migrate <pipeline>",
		Short: "Turn a single-file pipeline into a pipeline folder (with its history, and optionally its skills)",
		Long: `Move .ship/pipelines/<name>.yml to .ship/pipelines/<name>/pipeline.yml. The
folder also holds the pipeline's history (versions, run stats, feedback),
which moves over from .ship/history/<name>/, and can hold the pipeline's own
skills in skills/<skill>/SKILL.md.

--skills also moves the skills from .claude/skills that this pipeline calls
and no other pipeline here does. Skills in ~/.claude/skills are left alone:
pipelines in other repos may use them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			repo, err := cmdRepo(cmd)
			if err != nil {
				return err
			}
			l := a.loader(repo)
			path := l.Path(name)
			if path == "" {
				return fail(exitNotFound, "no pipeline %q in %s", name, strings.Join(l.Dirs, " or "))
			}
			if pipeline.FolderOf(path) != "" {
				return fail(exitConflict, "%s is already a folder (%s)", name, a.show(repo, filepath.Dir(path)))
			}
			f, findings := l.Load(name)
			if f == nil {
				return fail(exitUser, "can't load %s: %v", name, findings)
			}
			dir := l.Dir(name)
			folder := filepath.Join(dir, name)
			if _, err := os.Stat(folder); err == nil {
				return fail(exitConflict, "%s already exists; move it out of the way first", a.show(repo, folder))
			}
			oldHistory := engine.HistoryDir(l, repo, a.home, name)
			say := func(format string, args ...any) { fmt.Printf("  "+format+"\n", args...) }

			if err := os.MkdirAll(folder, 0o755); err != nil {
				return err
			}
			// The schema comment's relative path is one level deeper now.
			src := strings.Replace(string(f.Source), "$schema=../schema/", "$schema=../../schema/", 1)
			dest := filepath.Join(folder, pipeline.FolderFile)
			if err := os.WriteFile(dest, []byte(src), 0o644); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			say("moved    %s → %s", a.show(repo, path), a.show(repo, dest))

			// History: everything in the old dir moves into the folder.
			if entries, err := os.ReadDir(oldHistory); err == nil {
				for _, e := range entries {
					from, to := filepath.Join(oldHistory, e.Name()), filepath.Join(folder, e.Name())
					if _, err := os.Stat(to); err == nil {
						say("kept     %s (%s exists)", a.show(repo, from), a.show(repo, to))
						continue
					}
					if err := os.Rename(from, to); err != nil {
						return err
					}
				}
				if rest, _ := os.ReadDir(oldHistory); len(rest) == 0 {
					_ = os.Remove(oldHistory)
				}
				say("moved    %s/ → %s/", a.show(repo, oldHistory), a.show(repo, folder))
			}

			if skills {
				if repo == "" || dir != engine.PipelinesDir(repo) {
					say("skipped  --skills: %s is a global pipeline, and skills in ~/.claude/skills may be used by other repos", name)
				} else {
					for _, s := range a.ownSkills(l, repo, name, f) {
						from, to := filepath.Join(repo, ".claude", "skills", s), filepath.Join(folder, pipeline.SkillsDir, s)
						if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
							return err
						}
						if err := os.Rename(from, to); err != nil {
							return err
						}
						say("moved    %s/ → %s/", a.show(repo, from), a.show(repo, to))
					}
				}
			}
			if wrote, err := pipeline.EnsurePlugin(folder, name, f.Pipeline.Description); err != nil {
				return err
			} else if wrote {
				say("wrote    %s", a.show(repo, filepath.Join(folder, pipeline.PluginManifest)))
			}
			fmt.Printf("\n%s is now a pipeline folder. Its agents load the folder's skills (skills/<skill>/SKILL.md) as a plugin.\n", name)
			if repo != "" {
				fmt.Println("Commit the moves: runs check out your base branch, so they only see the folder once it's committed there.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&skills, "skills", false, "also move the repo skills only this pipeline uses into the folder")
	return cmd
}

// ownSkills lists the repo skills (.claude/skills/<s>/SKILL.md) that
// pipeline name calls and no other pipeline on l does.
func (a *app) ownSkills(l *pipeline.Loader, repo, name string, f *pipeline.File) []string {
	calls := func(f *pipeline.File) map[string]bool {
		out := map[string]bool{}
		for _, sn := range f.Pipeline.SortedSteps() {
			if s := f.Pipeline.Steps[sn]; s != nil {
				for _, line := range []*string{s.Agent, s.Split} {
					if n := pipeline.SkillName(pipeline.Str(line)); n != "" && !strings.Contains(n, ":") {
						out[n] = true
					}
				}
			}
		}
		return out
	}
	mine := calls(f)
	for _, other := range l.Names() {
		if other == name {
			continue
		}
		if of, _ := l.Load(other); of != nil {
			for s := range calls(of) {
				delete(mine, s)
			}
		}
	}
	var out []string
	for s := range mine {
		if st, err := os.Stat(filepath.Join(repo, ".claude", "skills", s, "SKILL.md")); err == nil && !st.IsDir() {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// show is a path as people see it: repo-relative, else with ~.
func (a *app) show(repo, p string) string {
	if repo != "" {
		if r, err := filepath.Rel(repo, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return tildify(p)
}
