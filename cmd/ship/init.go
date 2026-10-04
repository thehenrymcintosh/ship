package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/merlin-digital/ship/internal/brand"
	"github.com/merlin-digital/ship/internal/config"
	"github.com/merlin-digital/ship/internal/initfiles"
	"github.com/merlin-digital/ship/internal/pipeline"
)

func (a *app) initCmd() *cobra.Command {
	var skill, force, global bool
	var repoFlag string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create " + brand.Dir + "/ with starter pipelines, rules and schemas",
		Long: `Create ` + brand.Dir + `/ in the current repo with config.yml, pipelines/feature.yml and
slice.yml, rules/splitting.md, bin/pr-status and schema/*.json, and map the
schemas in .vscode/settings.json. Existing files are kept unless --force.

With --global, write the starter pipelines to ~/` + brand.Dir + `/pipelines instead, so
they're available in every repo (a repo pipeline of the same name wins).`,
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
			if skill && !global {
				files = append(files, initfiles.Skill())
			}
			// Schemas are generated: always refresh them.
			for _, f := range files {
				path := filepath.Join(root, f.Path)
				isSchema := filepath.Dir(path) == schemaDir
				if _, err := os.Stat(path); err == nil && !force && !isSchema {
					fmt.Printf("  kept     %s\n", f.Path)
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, f.Content, os.FileMode(f.Mode)); err != nil {
					return err
				}
				fmt.Printf("  wrote    %s\n", f.Path)
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
				if !skill {
					fmt.Printf("Tip: `%s init --skill` installs the handoff skill so Claude can start runs for you.\n", brand.Name)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&skill, "skill", false, "also install the Claude Code handoff skill (.claude/skills/"+brand.SkillName+")")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	cmd.Flags().BoolVar(&global, "global", false, "write starter pipelines to ~/"+brand.Dir+"/pipelines for every repo")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "repo path (default: the git repo of the current dir)")
	return cmd
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
