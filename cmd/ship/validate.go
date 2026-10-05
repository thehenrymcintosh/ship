package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/daemon"
	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
)

// validateOpts returns validation options for a repo's config.
func (a *app) validateOpts(repo string) pipeline.Options {
	cfg, err := config.Load(a.home, repo)
	if err != nil {
		cfg = config.Defaults()
	}
	in := history.Inputs{Repo: repo, ClaudeDir: config.ClaudeDir()}
	return pipeline.Options{AgentCheck: daemon.NewRegistry().Check, AgentBase: cfg.Agent, SkillExists: func(f *pipeline.File, skill string) bool {
		return !in.ResolveSkill(skill, f.Name, pipeline.FolderOf(f.Path)).Missing
	}}
}

// pipelineArg maps a pipeline folder given as a path to its pipeline file.
func pipelineArg(p string) string {
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		if f := pipeline.FindIn(filepath.Dir(filepath.Clean(p)), filepath.Base(filepath.Clean(p))); f != "" {
			return f
		}
	}
	return p
}

// loader returns the repo + global pipeline search path. repo may be "".
func (a *app) loader(repo string) *pipeline.Loader {
	var dirs []string
	if repo != "" {
		dirs = append(dirs, engine.PipelinesDir(repo))
	}
	return pipeline.NewLoader(append(dirs, engine.GlobalPipelinesDir(a.home))...)
}

// currentRepo is the repo of the cwd, or "" outside a repo.
func currentRepo() string {
	r, err := repoRoot("")
	if err != nil {
		return ""
	}
	return r
}

func (a *app) validateCmd() *cobra.Command {
	var global bool
	cmd := &cobra.Command{
		Use:   "validate [files…]",
		Short: "Validate pipeline files (default: every pipeline of this repo)",
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := currentRepo()
			opts := a.validateOpts(repo)
			var all []pipeline.Finding
			checked := 0
			if len(args) > 0 {
				for _, f := range args {
					_, fs := pipeline.ValidateFile(pipelineArg(f), opts, engine.GlobalPipelinesDir(a.home))
					all = append(all, fs...)
					checked++
				}
			} else {
				var l *pipeline.Loader
				var names []string
				if global || repo == "" {
					l = a.loader("")
					names = l.Names()
				} else {
					l = a.loader(repo)
					// Only the repo's own files; global ones are checked with --global.
					names = pipeline.NewLoader(engine.PipelinesDir(repo)).Names()
				}
				for _, n := range names {
					_, fs := l.Validate(n, opts)
					all = append(all, fs...)
					checked++
				}
				if checked == 0 {
					return fail(exitUser, "no pipelines found in %s", filepath.Join(l.Dirs[0]))
				}
			}
			pipeline.SortFindings(all)
			if a.json {
				if all == nil {
					all = []pipeline.Finding{}
				}
				printJSON(all)
			} else {
				cwd, _ := os.Getwd()
				for _, f := range all {
					if r, err := filepath.Rel(cwd, f.File); err == nil && !filepath.IsAbs(r) && len(r) < len(f.File) {
						f.File = r
					}
					line := f.String()
					if f.Severity == pipeline.SevError {
						fmt.Println(a.color("31", line))
					} else {
						fmt.Println(a.color("33", line))
					}
				}
				errs, warns := 0, 0
				for _, f := range all {
					if f.Severity == pipeline.SevError {
						errs++
					} else {
						warns++
					}
				}
				fmt.Fprintf(os.Stderr, "%d pipeline(s) checked: %d error(s), %d warning(s)\n", checked, errs, warns)
			}
			if pipeline.HasErrors(all) {
				return &exitError{exitUser, fmt.Errorf("validation failed")}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "validate the global pipelines in ~/.ship/pipelines")
	return cmd
}

func (a *app) schemaCmd() *cobra.Command {
	var cfg bool
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the pipeline JSON Schema",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfg {
				os.Stdout.Write(config.SchemaJSON())
			} else {
				os.Stdout.Write(pipeline.SchemaJSON())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&cfg, "config", false, "print the config schema instead")
	return cmd
}

func (a *app) graphCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "graph <pipeline|file>",
		Short: "Print a pipeline as a Mermaid flowchart",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var f *pipeline.File
			var fs []pipeline.Finding
			if _, err := os.Stat(args[0]); err == nil {
				f, fs = pipeline.ParseFile(pipelineArg(args[0]))
			} else {
				f, fs = a.loader(currentRepo()).Load(args[0])
			}
			if f == nil {
				for _, x := range fs {
					fmt.Fprintln(os.Stderr, x)
				}
				return fail(exitUser, "can't load %s", args[0])
			}
			if a.json {
				return printJSON(f.Pipeline.Graph())
			}
			fmt.Print(f.Pipeline.Mermaid())
			return nil
		},
	}
}
