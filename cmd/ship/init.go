package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/initfiles"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
)

func (a *app) initCmd() *cobra.Command {
	var noSkill, legacySkill, force, global bool
	var repoFlag string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up " + brand.Dir + "/ and the Claude Code skills in this repo",
		Long: `Create ` + brand.Dir + `/ in the current repo with an empty pipelines/ dir and the JSON
schemas, and map the schemas for VS Code (.vscode/settings.json) and
JetBrains IDEs (.idea/jsonSchemas.xml, wherever a .idea project exists).

It also installs three Claude Code skills in .claude/skills: /` + brand.DesignSkillName + `, which
you invoke to design a pipeline in plain language; ` + brand.SkillName + `, so you can
say "hand this to ` + brand.Name + `" at the end of a planning chat to start a run; and
` + brand.FeedbackSkillName + `, which records your critiques of a run's work for refining the
pipeline later.

No pipelines are added: design one with /` + brand.DesignSkillName + `, or copy a template with
` + "`" + brand.Name + ` templates` + "`" + ` and ` + "`" + brand.Name + ` add <template>` + "`" + `.

With --global, set up ~/` + brand.Dir + ` (for pipelines shared by every repo) and
install the skills for your user (~/.claude/skills).

Run it again to update the skills: copies you haven't edited are replaced
with this version's (` + "`" + brand.Name + ` update` + "`" + ` does this for you); edited ones are
kept unless --force.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, shipDir := a.home, a.home
			if !global {
				repo, err := repoRoot(repoFlag)
				if err != nil {
					return err
				}
				root, shipDir = repo, filepath.Join(repo, brand.Dir)
			}
			schemaDir := filepath.Join(shipDir, "schema")
			pipesDir := filepath.Join(shipDir, "pipelines")
			if err := os.MkdirAll(pipesDir, 0o755); err != nil {
				return err
			}
			// Schemas are generated: always refresh them.
			for _, f := range []initfiles.File{
				{Path: rel(root, filepath.Join(schemaDir, "pipeline.json")), Content: pipeline.SchemaJSON(), Mode: 0o644},
				{Path: rel(root, filepath.Join(schemaDir, "config.json")), Content: config.SchemaJSON(), Mode: 0o644},
			} {
				label := f.Path
				if global {
					label = filepath.Join(tildify(root), f.Path)
				}
				if err := writeStarter(root, f, label, true); err != nil {
					return err
				}
			}
			if !noSkill {
				// Repo skills go in <repo>/.claude/skills; global ones are
				// user-level skills, so they work in every repo.
				skillsDir := filepath.Join(root, ".claude", "skills")
				if global {
					skillsDir = filepath.Join(claudeDir(), "skills")
				}
				label := func(p string) string { return rel(root, p) }
				if global {
					label = tildify
				}
				if err := syncSkills(skillsDir, force, label, true); err != nil {
					return err
				}
			}
			// JetBrains IDEs: map the schemas in every .idea project found.
			ideaRoots := []string{root, filepath.Join(root, brand.Dir)}
			if global {
				ideaRoots = []string{root}
			}
			for _, pr := range ideaRoots {
				if fi, err := os.Stat(filepath.Join(pr, ".idea")); err != nil || !fi.IsDir() {
					continue
				}
				label := filepath.Join(tildify(pr), ".idea", "jsonSchemas.xml")
				if !global {
					label = rel(root, filepath.Join(pr, ".idea", "jsonSchemas.xml"))
				}
				switch done, err := mergeJetBrains(pr, schemaDir, ideaPatterns(pr, root, global)); {
				case err != nil:
					fmt.Fprintf(os.Stderr, "  skipped  %s: %v\n", label, err)
				case done:
					fmt.Printf("  updated  %s (JSON schema mappings)\n", label)
				default:
					fmt.Printf("  kept     %s\n", label)
				}
			}
			if !global {
				if err := mergeVSCode(root); err != nil {
					fmt.Fprintf(os.Stderr, "  skipped  .vscode/settings.json: %v\n", err)
				} else {
					fmt.Printf("  updated  .vscode/settings.json (yaml.schemas)\n")
				}
			}
			fmt.Println()
			where := brand.Dir + "/pipelines"
			if global {
				where = filepath.Join(tildify(a.home), "pipelines") + " (shared by every repo)"
			}
			fmt.Printf("Pipelines go in %s. To create one:\n", where)
			if !noSkill {
				fmt.Printf("  • in Claude Code, run /%s and describe your workflow\n", brand.DesignSkillName)
			}
			fmt.Printf("  • or start from a template: `%s templates`, then `%s add <template>`\n", brand.Name, brand.Name)
			if !noSkill {
				fmt.Printf("Then, after planning a change with Claude, say \"hand this to %s\" to start a run.\n", brand.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noSkill, "no-skill", false, "don't install the Claude Code skills")
	// --skill was the old opt-in; the skill is now installed by default.
	cmd.Flags().BoolVar(&legacySkill, "skill", false, "")
	cmd.Flags().MarkHidden("skill")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	cmd.Flags().BoolVar(&global, "global", false, "set up ~/"+brand.Dir+" and user-level skills instead of this repo")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "repo path (default: the git repo of the current dir)")
	return cmd
}

// syncSkills installs or updates the init skills in dir and prints what it
// did; quiet leaves out the ones already current.
func syncSkills(dir string, force bool, label func(string) string, verbose bool) error {
	results, err := initfiles.Sync(dir, force)
	for _, r := range results {
		switch r.Action {
		case initfiles.Wrote:
			fmt.Printf("  wrote    %s\n", label(r.Path))
		case initfiles.Updated:
			fmt.Printf("  updated  %s\n", label(r.Path))
		case initfiles.Edited:
			fmt.Printf("  kept     %s (you've edited it; --force replaces it)\n", label(r.Path))
		case initfiles.Unchanged:
			if verbose {
				fmt.Printf("  kept     %s (current)\n", label(r.Path))
			}
		}
	}
	return err
}

// refreshSkillsCmd updates the init skills wherever they're installed: for
// the user, and in every repo ship has runs for. `update` runs it with the
// new binary, which carries the new skills.
func (a *app) refreshSkillsCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "refresh-skills",
		Short:  "Update the skills `init` installed, wherever they are (run by `update`)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dirs := []string{filepath.Join(claudeDir(), "skills")}
			seen := map[string]bool{}
			var runs []*store.RunSnapshot
			if st, err := a.store(); err == nil {
				runs, _ = st.List()
			}
			for _, s := range runs {
				if s.Repo != "" && !seen[s.Repo] {
					seen[s.Repo] = true
					dirs = append(dirs, filepath.Join(s.Repo, ".claude", "skills"))
				}
			}
			for _, d := range dirs {
				if !initfiles.Installed(d) {
					continue
				}
				if err := syncSkills(d, false, tildify, false); err != nil {
					fmt.Fprintf(os.Stderr, "  skipped  %s: %v\n", tildify(d), err)
				}
			}
			return nil
		},
	}
}

// writeStarter writes one starter file under root unless it exists (or
// overwrite is set), printing what happened as label.
func writeStarter(root string, f initfiles.File, label string, overwrite bool) error {
	path := filepath.Join(root, f.Path)
	if _, err := os.Stat(path); err == nil && !overwrite {
		fmt.Printf("  kept     %s\n", label)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, f.Content, os.FileMode(f.Mode)); err != nil {
		return err
	}
	fmt.Printf("  wrote    %s\n", label)
	return nil
}

// claudeDir is Claude Code's user config dir.
func claudeDir() string { return config.ClaudeDir() }

func tildify(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if r, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.Join("~", r)
		}
	}
	return p
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return r
}

// mergeVSCode adds yaml.schemas mappings without touching other settings.
func mergeVSCode(repo string) error {
	path := filepath.Join(repo, ".vscode", "settings.json")
	settings := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &settings); err != nil {
			return fmt.Errorf("it isn't plain JSON (comments?); add yaml.schemas by hand")
		}
	}
	schemas, _ := settings["yaml.schemas"].(map[string]any)
	if schemas == nil {
		schemas = map[string]any{}
	}
	schemas["./"+brand.Dir+"/schema/pipeline.json"] = []string{brand.Dir + "/pipelines/*.yml", brand.Dir + "/pipelines/*.yaml"}
	schemas["./"+brand.Dir+"/schema/config.json"] = []string{brand.Dir + "/config.yml"}
	settings["yaml.schemas"] = schemas
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
