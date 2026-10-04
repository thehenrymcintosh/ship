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
)

func (a *app) initCmd() *cobra.Command {
	var noSkill, legacySkill, force, global bool
	var repoFlag string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create " + brand.Dir + "/ with starter pipelines, rules and schemas",
		Long: `Create ` + brand.Dir + `/ in the current repo with config.yml, pipelines/feature.yml and
slice.yml, rules/splitting.md, bin/pr-status and schema/*.json, and map the
schemas in .vscode/settings.json. It also installs the Claude Code handoff
skill (.claude/skills/` + brand.SkillName + `), so you can say "hand this to ` + brand.Name + `" at
the end of a planning chat. Existing files are kept unless --force.

With --global, write the starter pipelines to ~/` + brand.Dir + `/pipelines instead, so
they're available in every repo (a repo pipeline of the same name wins), and
install the skill for your user (~/.claude/skills) so it works everywhere.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var root string
			var files []initfiles.File
			schemaDir := ""
			if global {
				root = a.home
				files = initfiles.Global(a.home)
				schemaDir = filepath.Join(a.home, "schema")
			} else {
				repo, err := repoRoot(repoFlag)
				if err != nil {
					return err
				}
				root = repo
				files = initfiles.Repo()
				schemaDir = filepath.Join(repo, brand.Dir, "schema")
			}
			files = append(files,
				initfiles.File{Path: rel(root, filepath.Join(schemaDir, "pipeline.json")), Content: pipeline.SchemaJSON(), Mode: 0o644},
				initfiles.File{Path: rel(root, filepath.Join(schemaDir, "config.json")), Content: config.SchemaJSON(), Mode: 0o644},
			)
			// Schemas are generated: always refresh them.
			for _, f := range files {
				isSchema := filepath.Dir(filepath.Join(root, f.Path)) == schemaDir
				if err := writeStarter(root, f, f.Path, force || isSchema); err != nil {
					return err
				}
			}
			if !noSkill {
				sk := initfiles.Skill()
				if global {
					// User-level skills work in every repo.
					dir := claudeDir()
					sk.Path = filepath.Join("skills", brand.SkillName, "SKILL.md")
					if err := writeStarter(dir, sk, filepath.Join(tildify(dir), sk.Path), force); err != nil {
						return err
					}
				} else if err := writeStarter(root, sk, sk.Path, force); err != nil {
					return err
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
			if global {
				fmt.Printf("Global pipelines are in %s. They work in any repo: %s start feature --brief brief.md\n", filepath.Join(a.home, "pipelines"), brand.Name)
			} else {
				fmt.Printf("Next: edit %s/pipelines/*.yml, then `%s validate`.\n", brand.Dir, brand.Name)
			}
			if !noSkill {
				fmt.Printf("In Claude Code, finish planning and say \"hand this to %s\" to start a run.\n", brand.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noSkill, "no-skill", false, "don't install the Claude Code handoff skill")
	// --skill was the old opt-in; the skill is now installed by default.
	cmd.Flags().BoolVar(&legacySkill, "skill", false, "")
	cmd.Flags().MarkHidden("skill")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	cmd.Flags().BoolVar(&global, "global", false, "write starter pipelines to ~/"+brand.Dir+"/pipelines for every repo")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "repo path (default: the git repo of the current dir)")
	return cmd
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

// claudeDir is Claude Code's user config dir: $CLAUDE_CONFIG_DIR or ~/.claude.
func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

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
