package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/templates"
)

func (a *app) templateSources() templates.Sources {
	return templates.Sources{UserDir: filepath.Join(a.home, "templates")}
}

func (a *app) templatesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "templates",
		Short: "List pipeline templates (add one with `" + brand.Name + " add <template>`)",
		Long: `List the pipeline templates ` + "`" + brand.Name + ` add` + "`" + ` can copy in: the built-in ones, plus
your own in ~/` + brand.Dir + `/templates/<name>/ (which win on a name clash).

A template is a directory with a template.yml (description: …) and any of
pipelines/, bin/, rules/ and skills/<skill>/, copied into ` + brand.Dir + `/ and
.claude/skills/.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			all, err := templates.List(a.templateSources())
			if err != nil {
				return err
			}
			if a.json {
				type row struct {
					Name        string   `json:"name"`
					Description string   `json:"description"`
					Source      string   `json:"source"`
					Pipelines   []string `json:"pipelines"`
				}
				rows := []row{}
				for _, t := range all {
					rows = append(rows, row{t.Name, t.Description, t.Source, t.Pipelines()})
				}
				return printJSON(rows)
			}
			if len(all) == 0 {
				fmt.Printf("No templates yet. Add your own as ~/%s/templates/<name>/ (template.yml plus pipelines/, bin/, rules/, skills/),\n", brand.Dir)
				fmt.Printf("or design a pipeline from scratch with /%s in Claude Code.\n", brand.DesignSkillName)
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TEMPLATE\tPIPELINES\tDESCRIPTION\tSOURCE")
			for _, t := range all {
				src := t.Source
				if src != "built-in" {
					src = tildify(src)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, strings.Join(t.Pipelines(), ", "), t.Description, src)
			}
			return tw.Flush()
		},
	}
}

func (a *app) addCmd() *cobra.Command {
	var global, force bool
	var repoFlag string
	cmd := &cobra.Command{
		Use:   "add <template>",
		Short: "Copy a pipeline template (and the scripts, rules and skills it uses) into this repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tpl, err := templates.Get(a.templateSources(), args[0])
			if errors.Is(err, templates.ErrNotFound) {
				return fail(exitNotFound, "%v (see `%s templates`)", err, brand.Name)
			}
			if err != nil {
				return err
			}
			root, shipDir, skillsDir := a.home, a.home, filepath.Join(claudeDir(), "skills")
			repo := ""
			if !global {
				if repo, err = repoRoot(repoFlag); err != nil {
					return err
				}
				root, shipDir, skillsDir = repo, filepath.Join(repo, brand.Dir), filepath.Join(repo, ".claude", "skills")
			}
			results, err := tpl.Install(shipDir, skillsDir, force)
			for _, r := range results {
				label := tildify(r.Path)
				if !global {
					label = rel(root, r.Path)
				}
				if r.Written {
					fmt.Printf("  wrote    %s\n", label)
				} else {
					fmt.Printf("  kept     %s (exists; --force to overwrite)\n", label)
				}
			}
			if err != nil {
				return err
			}
			// Check what was added, resolving fanout targets like a run would.
			l := pipeline.NewLoader(engine.PipelinesDir(repo), engine.GlobalPipelinesDir(a.home))
			if global {
				l = pipeline.NewLoader(engine.GlobalPipelinesDir(a.home))
			}
			opts := a.validateOpts(repo)
			var findings []pipeline.Finding
			for _, n := range tpl.Pipelines() {
				_, fs := l.Validate(n, opts)
				findings = append(findings, fs...)
			}
			for _, f := range findings {
				f.File = tildify(f.File)
				fmt.Println(a.color("33", f.String()))
			}
			fmt.Println()
			if pipeline.HasErrors(findings) {
				return fail(exitUser, "template %s added, but its pipelines have errors (above)", tpl.Name)
			}
			if !global {
				fmt.Printf("Commit these files: runs work in git worktrees checked out from your base branch, so agents only see committed skills, scripts and rules.\n")
			}
			names := tpl.Pipelines()
			if len(names) > 0 {
				fmt.Printf("Added %s. Start a run with `%s start %s --brief brief.md`, or adapt it with /%s.\n", tpl.Name, brand.Name, names[0], brand.DesignSkillName)
			} else {
				fmt.Printf("Added %s.\n", tpl.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "add to ~/"+brand.Dir+" (pipelines shared by every repo) and user-level skills")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite files that already exist")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "repo path (default: the git repo of the current dir)")
	return cmd
}
