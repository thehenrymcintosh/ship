package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/engine/steps"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
)

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func money(f float64) string {
	if f <= 0 {
		return ""
	}
	return fmt.Sprintf("$%.2f", f)
}

func (a *app) lsCmd() *cobra.Command {
	var all, pipelines bool
	var repoFlag, status string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List runs (active by default) or pipelines",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if pipelines {
				return a.listPipelines(repoFlag)
			}
			st, err := a.store()
			if err != nil {
				return err
			}
			snaps, err := st.List()
			if err != nil {
				return err
			}
			repo := ""
			if repoFlag != "" {
				if repo, err = repoRoot(repoFlag); err != nil {
					return err
				}
			}
			var out []*store.RunSnapshot
			for _, s := range snaps {
				if !all && status == "" && s.Status.Terminal() {
					continue
				}
				if status != "" && string(s.Status) != status {
					continue
				}
				if repo != "" && s.Repo != repo {
					continue
				}
				out = append(out, s)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
			if a.json {
				lite := make([]store.RunSnapshot, len(out))
				for i, s := range out {
					lite[i] = s.Lite()
				}
				return printJSON(lite)
			}
			if len(out) == 0 {
				if all {
					fmt.Println("No runs yet.")
				} else {
					fmt.Println("No active runs. (`" + brand.Name + " ls --all` shows finished runs.)")
				}
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATUS\tSTEP\tPIPELINE\tTITLE\tCOST\tCREATED")
			for _, s := range out {
				id := s.ID
				if s.Parent != nil {
					id = "  ." + strings.TrimPrefix(s.ID, s.Parent.ID+".")
				}
				bell := ""
				if s.Status.InInbox() {
					bell = " 🔔"
				}
				step := s.CurrentStep
				if s.WaitingOn != "" {
					step = "after " + shortRef(s.WaitingOn)
				}
				fmt.Fprintf(tw, "%s\t%s%s\t%s\t%s\t%s\t%s\t%s\n", id, a.status(s.Status), bell, step, s.Pipeline, truncate(s.Title, 48), money(s.CostUSD), ago(s.CreatedAt))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include finished runs")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "only runs of this repo")
	cmd.Flags().StringVar(&status, "status", "", "only runs with this status")
	cmd.Flags().BoolVar(&pipelines, "pipelines", false, "list the pipelines available here (repo and global)")
	return cmd
}

func (a *app) listPipelines(repoFlag string) error {
	repo := ""
	if repoFlag != "" {
		r, err := repoRoot(repoFlag)
		if err != nil {
			return err
		}
		repo = r
	} else {
		repo = currentRepo()
	}
	l := a.loader(repo)
	type row struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"` // repo | global
		Path        string `json:"path"`
		Valid       bool   `json:"valid"`
		Default     bool   `json:"default,omitempty"` // used when a start names none
	}
	var rows []row
	opts := a.validateOpts(repo)
	cfg, _ := config.Load(a.home, repo)
	for _, n := range l.Names() {
		f, fs := l.Validate(n, opts)
		r := row{Name: n, Path: l.Path(n), Valid: !pipeline.HasErrors(fs), Source: "repo", Default: n == cfg.DefaultPipeline}
		if repo == "" || l.Dir(n) != engine.PipelinesDir(repo) {
			r.Source = "global"
		}
		if f != nil {
			r.Description = f.Pipeline.Description
		}
		rows = append(rows, r)
	}
	if a.json {
		if rows == nil {
			rows = []row{}
		}
		return printJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Printf("No pipelines yet. Design one with /%s in Claude Code, or start from a template: `%s templates`, then `%s add <template>`.\n", brand.DesignSkillName, brand.Name, brand.Name)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PIPELINE\tSOURCE\tDESCRIPTION")
	for _, r := range rows {
		name := r.Name
		if r.Default {
			name += a.color("32", " (default)")
		}
		if !r.Valid {
			name += a.color("31", " (invalid)")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, r.Source, r.Description)
	}
	return tw.Flush()
}

// defaultPipelineCmd shows or sets the pipeline a start uses when it names
// none.
func (a *app) defaultPipelineCmd() *cobra.Command {
	var global, clear bool
	cmd := &cobra.Command{
		Use:   "default [pipeline]",
		Short: "Show or set the pipeline `start` uses when neither it nor the brief names one",
		Long: `With a name, set default_pipeline in this repo's ` + brand.Dir + `/config.yml (or, with
--global, ~/` + brand.Dir + `/config.yml). Without one, print the current default.
--clear removes the setting.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := cmdRepo(cmd)
			if err != nil {
				return err
			}
			if len(args) == 0 && !clear {
				cfg, err := config.Load(a.home, repo)
				if err != nil {
					return err
				}
				if a.json {
					return printJSON(map[string]string{"default_pipeline": cfg.DefaultPipeline})
				}
				if cfg.DefaultPipeline == "" {
					fmt.Println("No default pipeline.")
				} else {
					fmt.Println(cfg.DefaultPipeline)
				}
				return nil
			}
			file := config.UserFile(a.home)
			if !global {
				if repo == "" {
					return fail(exitUser, "not in a git repo (use --global for every repo)")
				}
				file = config.RepoFile(repo)
			}
			name := ""
			if len(args) > 0 && !clear {
				name = args[0]
				if l := a.loader(repo); l.Path(name) == "" {
					return fail(exitNotFound, "no pipeline %q here (have: %s)", name, strings.Join(l.Names(), ", "))
				}
			}
			if err := config.SetDefaultPipeline(file, name); err != nil {
				return err
			}
			if name == "" {
				fmt.Printf("Cleared the default pipeline in %s\n", tildify(file))
			} else {
				fmt.Printf("Default pipeline is now %s (%s)\n", a.bold(name), tildify(file))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "set it for every repo (~/"+brand.Dir+"/config.yml)")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the default")
	return cmd
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <run>",
		Short: "Show a run: status, current step, visits, pending ask, worktree, children",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.resolveRun(args[0])
			if err != nil {
				return err
			}
			s, err := a.loadRun(id)
			if err != nil {
				return err
			}
			if a.json {
				return printJSON(s)
			}
			fmt.Printf("%s  %s\n", a.bold(s.Title), a.status(s.Status))
			fmt.Printf("%s\n", a.dim(s.ID))
			if s.StatusReason != "" {
				fmt.Printf("reason:    %s\n", s.StatusReason)
			}
			if s.WaitingOn != "" {
				stack := ""
				if s.AfterStack {
					stack = ", on its branch"
				}
				fmt.Printf("after:     %s (starts once it's done%s; `%s start-now %s` starts it now)\n", s.WaitingOn, stack, brand.Name, shortRef(s.ID))
			} else if s.After != "" {
				fmt.Printf("after:     %s\n", s.After)
			}
			fmt.Printf("pipeline:  %s · step %s · %d transitions\n", s.Pipeline, s.CurrentStep, s.Transitions)
			if s.Branch != "" {
				fmt.Printf("branch:    %s ← %s\n", s.Branch, s.Base)
			}
			if s.Workspace != nil {
				missing := ""
				if s.WorkspaceMissing {
					missing = a.color("31", " (missing)")
				}
				fmt.Printf("worktree:  %s%s\n", s.Workspace.Path, missing)
			}
			if s.PR != nil && s.PR.URL != "" {
				fmt.Printf("pr:        %s", s.PR.URL)
				if s.PR.State != "" {
					fmt.Print(" " + a.dim(s.PR.State))
				}
				fmt.Println()
			}
			if c := money(s.CostUSD); c != "" {
				fmt.Printf("cost:      %s\n", c)
			}
			if s.Tokens > 0 {
				fmt.Printf("tokens:    %s\n", steps.FormatTokens(s.Tokens))
			}
			if s.BaseMoved {
				fmt.Println(a.color("33", "base moved: the parent slice changed, so a restack may be needed"))
			}
			if s.PermissionDenials > 0 {
				fmt.Println(a.color("33", fmt.Sprintf("%d permission denials: consider allowed_tools", s.PermissionDenials)))
			}
			if len(s.Vars) > 0 {
				fmt.Println("\nvariables:")
				keys := make([]string, 0, len(s.Vars))
				for k := range s.Vars {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Printf("  %s = %s\n", k, s.Vars[k])
				}
			}
			if len(s.RunNotes) > 0 {
				fmt.Println("\nnotes for this run (every later agent step gets them):")
				for _, n := range s.RunNotes {
					fmt.Printf("  %s %s\n", a.dim(n.Step+":"), strings.ReplaceAll(n.Note, "\n", "\n    "))
				}
			}
			if len(s.Visits) > 0 {
				fmt.Println("\nvisits:")
				tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
				for _, v := range s.Visits {
					outcome := v.Outcome
					switch {
					case v.Interrupted:
						outcome = a.color("33", "interrupted")
					case v.Queued:
						outcome = a.dim("queued")
					case v.Running() && s.PendingAsk != nil && s.PendingAsk.Seq == v.Seq:
						outcome = a.color("35", "waiting for you")
					case v.Running():
						outcome = a.color("34", "running")
					}
					dur := ""
					if v.DurationMS > 0 {
						dur = (time.Duration(v.DurationMS) * time.Millisecond).Round(time.Second).String()
					}
					tok := ""
					if v.Tokens > 0 {
						tok = steps.FormatTokens(v.Tokens) + " tok"
					}
					fmt.Fprintf(tw, "  %d\t%s %s\t%s\t%s\t%s\t%s\t%s\n", v.Seq, pipeline.TypeGlyph(v.Type), v.Step, outcome, dur, money(v.CostUSD), tok, truncate(firstLine(v.Summary), 70))
				}
				tw.Flush()
			}
			if card := a.loadCard(s.ID); card != nil {
				fmt.Println()
				a.printCard(os.Stdout, card, s.ID)
			} else if pa := s.PendingAsk; pa != nil {
				fmt.Printf("\n%s %s\n", a.color("35", "●"), a.bold(pa.Question))
				if len(pa.Choices) > 0 {
					fmt.Printf("  choices: %s\n", strings.Join(pa.Choices, " · "))
				}
				fmt.Printf("  answer:  %s answer %s <choice> [--note … [--for run]]\n", brand.Name, shortRef(s.ID))
			}
			if len(s.Slices) > 0 || len(s.Children) > 0 {
				fmt.Println("\nslices:")
				started := map[int]store.ChildRef{}
				for _, c := range s.Children {
					started[c.Number] = c
				}
				for _, sl := range s.Slices {
					c, ok := started[sl.Number]
					if !ok {
						fmt.Printf("  %d. %s  %s\n", sl.Number, sl.Key, a.dim("pending"))
						continue
					}
					cs, _ := a.loadRun(c.ID)
					step := ""
					st := c.Status
					if cs != nil {
						step, st = cs.CurrentStep, cs.Status
					}
					fmt.Printf("  %d. %s  %s  %s\n", c.Number, c.SliceKey, a.status(st), step)
				}
				if len(s.Children) < len(s.Slices) && !s.Status.Terminal() {
					fmt.Printf("  start a pending slice now: %s start-now %s --slice <n>\n", brand.Name, shortRef(s.ID))
				}
			}
			return nil
		},
	}
}

func shortRef(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 && !strings.Contains(id[i:], ".") {
		return id[i+1:]
	}
	return id
}

func (a *app) logsCmd() *cobra.Command {
	var follow bool
	var stream string
	cmd := &cobra.Command{
		Use:   "logs <run> [step|seq]",
		Short: "Show a visit's output (default: the current or latest visit)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.resolveRun(args[0])
			if err != nil {
				return err
			}
			s, err := a.loadRun(id)
			if err != nil {
				return err
			}
			v := s.LastVisit()
			if len(args) == 2 {
				v = nil
				if n, err := strconv.Atoi(args[1]); err == nil {
					v = s.Visit(n)
				} else {
					for i := len(s.Visits) - 1; i >= 0; i-- {
						if s.Visits[i].Step == args[1] {
							v = &s.Visits[i]
							break
						}
					}
				}
			}
			if v == nil {
				return fail(exitNotFound, "no such visit")
			}
			st, _ := a.store()
			dir := filepath.Join(st.RunDir(id), store.VisitsDir, v.Dir)
			files := map[string]string{"stdout": "stdout.log", "stderr": "stderr.log", "agent": "transcript.jsonl", "handover": "handover.md"}
			name := ""
			if stream != "" {
				name = files[stream]
				if name == "" {
					return fail(exitUser, "--stream is one of stdout, stderr, agent, handover")
				}
			} else if v.Type == pipeline.TypeAgent || v.Type == pipeline.TypeSplit {
				name = "transcript.jsonl"
			} else {
				name = "stdout.log"
			}
			fmt.Fprintf(os.Stderr, "%s\n", a.dim(fmt.Sprintf("── %d %s · %s ──", v.Seq, v.Step, name)))
			path := filepath.Join(dir, name)
			pretty := name == "transcript.jsonl" && !a.json
			var offset int64
			for {
				offset = a.printFrom(path, offset, pretty)
				if !follow {
					return nil
				}
				cur, err := a.loadRun(id)
				if err != nil {
					return err
				}
				if cv := cur.Visit(v.Seq); cv == nil || !cv.Running() {
					a.printFrom(path, offset, pretty)
					if cv != nil {
						fmt.Fprintf(os.Stderr, "%s\n", a.dim("── finished: "+cv.Outcome+" ──"))
					}
					return nil
				}
				time.Sleep(300 * time.Millisecond)
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing until the visit finishes")
	cmd.Flags().StringVar(&stream, "stream", "", "stdout | stderr | agent | handover")
	return cmd
}

// printFrom prints a file from offset and returns the new offset.
func (a *app) printFrom(path string, offset int64, pretty bool) int64 {
	f, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer f.Close()
	f.Seek(offset, io.SeekStart)
	b, _ := io.ReadAll(f)
	if pretty {
		// Only print whole lines; keep a partial line for next time.
		end := strings.LastIndexByte(string(b), '\n')
		if end < 0 {
			return offset
		}
		for _, line := range strings.Split(string(b[:end]), "\n") {
			if s := transcriptLine(line); s != "" {
				fmt.Println(s)
			}
		}
		return offset + int64(end) + 1
	}
	os.Stdout.Write(b)
	return offset + int64(len(b))
}

func (a *app) cdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cd <run>",
		Short: `Print a run's worktree path (for cd "$(` + brand.Name + ` cd 3fa)")`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.resolveRun(args[0])
			if err != nil {
				return err
			}
			s, err := a.loadRun(id)
			if err != nil {
				return err
			}
			if s.Workspace == nil {
				return fail(exitConflict, "run %s has no worktree (released or not acquired yet)", id)
			}
			fmt.Println(s.Workspace.Path)
			return nil
		},
	}
}
