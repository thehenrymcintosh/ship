package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/daemon"
	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/refine"
)

// engine builds an engine for local, non-run work (versions, feedback).
// It starts nothing.
func (a *app) engine() (*engine.Engine, error) {
	opts, err := daemon.EngineOptions(a.home, nil, os.Environ(), false)
	if err != nil {
		return nil, err
	}
	return engine.New(opts), nil
}

func (a *app) feedbackCmd() *cobra.Command {
	var step, pipe, source, repoFlag string
	cmd := &cobra.Command{
		Use:   `feedback [run] "what was good or bad"`,
		Short: "Record feedback on a run, one of its steps, or a pipeline",
		Long: `Record feedback on the work a pipeline did. It's stored against the exact
pipeline version that did the work (the pipeline plus the skills, rules and
scripts it uses), and ` + "`" + brand.Name + ` pipeline refine` + "`" + ` turns open feedback into a proposed
next version.

  ` + brand.Name + ` feedback 3fa "the PR description didn't explain why"
  ` + brand.Name + ` feedback 3fa --step docs "too long, and no examples"
  ` + brand.Name + ` feedback --pipeline pr "review keeps missing error handling"

You can also comment on a PR a run opened, starting the comment with "` + brand.Name + `:".`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := args[len(args)-1]
			if strings.TrimSpace(text) == "" {
				return fail(exitUser, "feedback text is empty")
			}
			if source == "" {
				source = history.FromCLI
			}
			if len(args) == 2 {
				if pipe != "" {
					return fail(exitUser, "give a run or --pipeline, not both")
				}
				id, err := a.resolveRun(args[0])
				if err != nil {
					return err
				}
				// Through the daemon if it runs (it validates the step); else directly.
				if c, err := daemon.Connect(a.home); err == nil {
					var f history.Feedback
					if err := c.Do("POST", "/api/runs/"+id+"/feedback", map[string]string{"step": step, "text": text, "source": source}, &f); err != nil {
						return err
					}
					return a.printRecorded(f)
				}
				st, err := a.store()
				if err != nil {
					return err
				}
				snap, err := st.Load(id)
				if err != nil {
					return err
				}
				f, _, err := engine.RecordFeedback(snap, a.home, history.Feedback{Step: step, Text: text, Source: source})
				if err != nil {
					return err
				}
				return a.printRecorded(f)
			}
			if pipe == "" {
				return fail(exitUser, "say which run the feedback is about (or --pipeline <name> for the pipeline in general)")
			}
			e, err := a.engine()
			if err != nil {
				return err
			}
			repo := ""
			if repoFlag != "" || currentRepo() != "" {
				if repo, err = repoRoot(repoFlag); err != nil {
					return err
				}
			}
			hs, _, v, err := e.PipelineHistory(repo, pipe)
			if err != nil {
				return fail(exitNotFound, "%v", err)
			}
			f, _, err := hs.Add(history.Feedback{Version: v.Version, Hash: v.Hash, Step: step, Text: text, Source: source, Author: engine.GitAuthor(repo)})
			if err != nil {
				return err
			}
			return a.printRecorded(f)
		},
	}
	cmd.Flags().StringVar(&step, "step", "", "the step the feedback is about")
	cmd.Flags().StringVar(&pipe, "pipeline", "", "feedback on a pipeline in general, rather than one run")
	cmd.Flags().StringVar(&source, "source", "", "")
	cmd.Flags().MarkHidden("source") // the ship-feedback skill passes "claude"
	cmd.Flags().StringVar(&repoFlag, "repo", "", "repo path (with --pipeline)")
	return cmd
}

func (a *app) printRecorded(f history.Feedback) error {
	if a.json {
		return printJSON(f)
	}
	where := ""
	if f.Version > 0 {
		where = fmt.Sprintf(" against v%d", f.Version)
	}
	fmt.Printf("Recorded feedback #%d%s.\n", f.ID, where)
	return nil
}

// --- ship pipeline ------------------------------------------------------------

func (a *app) pipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Pipeline versions, feedback and stats: versions, feedback, refine, report, close, stats",
	}
	cmd.PersistentFlags().String("repo", "", "repo path (default: the git repo of the current dir)")
	cmd.AddCommand(a.versionsCmd(), a.pipelineFeedbackCmd(), a.closeCmd(), a.refineCmd(), a.reportCmd(), a.statsCmd(), a.migrateCmd(), a.diffCmd(), a.restoreCmd())
	return cmd
}

// pipelineCtx resolves a pipeline and registers its current version (for
// commands that record something).
func (a *app) pipelineCtx(cmd *cobra.Command, name string) (*engine.Engine, string, *history.Store, history.Version, error) {
	e, err := a.engine()
	if err != nil {
		return nil, "", nil, history.Version{}, err
	}
	repo, err := cmdRepo(cmd)
	if err != nil {
		return nil, "", nil, history.Version{}, err
	}
	hs, _, v, err := e.PipelineHistory(repo, name)
	if err != nil {
		return nil, "", nil, history.Version{}, fail(exitNotFound, "%v", err)
	}
	return e, repo, hs, v, nil
}

// pipelineRead resolves a pipeline without writing anything (for
// read-only commands); now is the pipeline as its files are now.
func (a *app) pipelineRead(cmd *cobra.Command, name string) (*engine.Engine, string, *history.Store, history.Fingerprint, error) {
	e, err := a.engine()
	if err != nil {
		return nil, "", nil, history.Fingerprint{}, err
	}
	repo, err := cmdRepo(cmd)
	if err != nil {
		return nil, "", nil, history.Fingerprint{}, err
	}
	hs, now, err := e.OpenPipelineHistory(repo, name)
	if err != nil {
		return nil, "", nil, history.Fingerprint{}, fail(exitNotFound, "%v", err)
	}
	return e, repo, hs, now, nil
}

func (a *app) versionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "versions <pipeline>",
		Short: "List a pipeline's versions and the feedback on each",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, hs, cur, err := a.pipelineRead(cmd, args[0])
			if err != nil {
				return err
			}
			vs, err := hs.Versions()
			if err != nil {
				return err
			}
			items, _ := hs.Items()
			if a.json {
				return printJSON(vs)
			}
			count := map[int]int{}
			for _, it := range items {
				count[it.Version]++
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "VERSION\tDATE\tSOURCE\tFEEDBACK\tCHANGE")
			recorded := false
			for _, v := range vs {
				label := v.Label()
				if v.Hash == cur.Hash {
					label += " ←"
					recorded = true
				}
				change := v.Summary
				if change == "" {
					change = strings.Join(v.Changed, "; ")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", label, v.At.Local().Format("2006-01-02"), v.Source, count[v.Version], truncate(change, 80))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if !recorded {
				fmt.Println(a.dim(fmt.Sprintf("The files as they are now (%s) aren't a recorded version yet; the next run records them.", cur.Short())))
			}
			return nil
		},
	}
}

func (a *app) pipelineFeedbackCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "feedback <pipeline>",
		Short: "List open feedback on a pipeline (--all for addressed and closed too)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, hs, _, err := a.pipelineRead(cmd, args[0])
			if err != nil {
				return err
			}
			items, err := hs.Items()
			if err != nil {
				return err
			}
			var shown []history.Item
			for _, it := range items {
				if all || it.Status == "open" {
					shown = append(shown, it)
				}
			}
			if a.json {
				if shown == nil {
					shown = []history.Item{}
				}
				return printJSON(shown)
			}
			if len(shown) == 0 {
				fmt.Printf("No open feedback on %s. Record some with `%s feedback <run> \"…\"`.\n", args[0], brand.Name)
				return nil
			}
			for _, it := range shown {
				where := fmt.Sprintf("v%d", it.Version)
				if it.Run != "" {
					where += " · " + shortRef(it.Run)
				}
				if it.Step != "" {
					where += " · " + it.Step
				}
				status := ""
				switch it.Status {
				case "addressed":
					status = a.color("32", fmt.Sprintf(" (addressed in v%d)", it.AddressedIn))
				case "closed":
					status = a.dim(" (closed)")
				}
				fmt.Printf("#%-3d %s%s\n     %s\n", it.ID, a.dim(where+" · "+it.Source), status, it.Text)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include addressed and closed feedback")
	return cmd
}

func (a *app) closeCmd() *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "close <pipeline> <feedback-id>…",
		Short: "Mark feedback as dealt with (e.g. after fixing it by hand)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, hs, _, err := a.pipelineCtx(cmd, args[0])
			if err != nil {
				return err
			}
			for _, s := range args[1:] {
				var id int
				if _, err := fmt.Sscan(strings.TrimPrefix(s, "#"), &id); err != nil {
					return fail(exitUser, "%q isn't a feedback id", s)
				}
				if err := hs.Close(id, note); err != nil {
					return fail(exitNotFound, "%v", err)
				}
				fmt.Printf("Closed #%d.\n", id)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "why it's closed")
	return cmd
}

func (a *app) refineContext(cmd *cobra.Command, name, model, fake string) (refine.Context, string, error) {
	e, repo, hs, v, err := a.pipelineCtx(cmd, name)
	if err != nil {
		return refine.Context{}, "", err
	}
	cfg, _ := config.Load(a.home, repo)
	if model == "" {
		model = cfg.Agent.Model
	}
	// Refine reads and edits the files in your checkout, so use freshly
	// computed parts (a version first recorded by a run points at its worktree).
	if closure, err := e.Loader(repo).Closure(name); err == nil {
		v.Parts = e.Fingerprint(repo, closure).Parts
	}
	c := refine.Context{
		Pipeline: name, Repo: repo, Home: a.home, ClaudeDir: config.ClaudeDir(),
		Store: hs, Current: v, Agents: e.Agents(), Model: model, Env: os.Environ(),
		RunsPerVersion: a.runsPerVersion(repo, name),
	}
	if fake != "" {
		abs, _ := filepath.Abs(fake)
		c.CLI, c.Env = "fake", append(c.Env, brand.EnvPrefix+"FAKE_AGENT="+abs)
	}
	return c, repo, nil
}

func (a *app) runsPerVersion(repo, name string) map[int]int {
	out := map[int]int{}
	st, err := a.store()
	if err != nil {
		return out
	}
	snaps, _ := st.List()
	for _, s := range snaps {
		if s.Pipeline == name && (repo == "" || s.Repo == repo) && s.PipelineVersion > 0 {
			out[s.PipelineVersion]++
		}
	}
	return out
}

func (a *app) refineCmd() *cobra.Command {
	var apply, keep bool
	var model, fake string
	cmd := &cobra.Command{
		Use:   "refine <pipeline>",
		Short: "Propose a new version of a pipeline from its open feedback",
		Long: `Have Claude read the open feedback on a pipeline, plus the pipeline and the
skills, rules and scripts it uses, and propose a new version. Each change says
which feedback it addresses; feedback it chose not to act on is listed with
reasons. You see the diff, then apply it, keep it to do yourself, or discard
it. Proposals are saved under ` + brand.Dir + `/history/<pipeline>/proposals/.

Applying records a new version that addresses that feedback. Feedback from
later runs is recorded against it, so ` + "`" + brand.Name + ` pipeline report` + "`" + ` can tell whether
the change helped.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, repo, err := a.refineContext(cmd, args[0], model, fake)
			if err != nil {
				return err
			}
			items, _ := c.Store.Items()
			open := 0
			for _, it := range items {
				if it.Status == "open" {
					open++
				}
			}
			if open == 0 {
				fmt.Printf("No open feedback on %s %s, so nothing to refine. Record some with `%s feedback`.\n", args[0], c.Current.Label(), brand.Name)
				return nil
			}
			fmt.Printf("Asking Claude to refine %s %s using %d piece(s) of open feedback…\n", args[0], c.Current.Label(), open)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			p, err := refine.Propose(ctx, c)
			if err != nil {
				return err
			}
			display := func(abs string) string {
				if repo != "" {
					if r, err := filepath.Rel(repo, abs); err == nil && !strings.HasPrefix(r, "..") {
						return r
					}
				}
				return tildify(abs)
			}
			fmt.Printf("\n%s\n", a.bold(p.Summary))
			if len(p.Changes) == 0 {
				fmt.Println("\nNo changes proposed.")
			}
			for _, ch := range p.Changes {
				fmt.Printf("\n%s  %s\n%s\n", a.bold(display(ch.Path)), a.dim(fmt.Sprintf("addresses %v", ch.Addresses)), ch.Why)
				a.showDiff(ch.Path, ch.Content)
			}
			if len(p.LeftAlone) > 0 {
				fmt.Println("\nLeft alone:")
				for _, s := range p.LeftAlone {
					fmt.Printf("  #%d  %s\n", s.ID, s.Why)
				}
			}
			for _, f := range p.Findings {
				fmt.Println(a.color("33", f.String()))
			}
			if p.CostUSD > 0 {
				fmt.Println(a.dim(fmt.Sprintf("(cost %s)", money(p.CostUSD))))
			}
			if len(p.Changes) == 0 {
				return nil
			}
			md, err := refine.Save(p, c.Store, display)
			if err != nil {
				return err
			}
			choice := "k"
			switch {
			case apply:
				choice = "a"
			case keep || !isTTY(os.Stdin):
			default:
				fmt.Printf("\n[a]pply, [k]eep the proposal to do it yourself, or [d]iscard? ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				choice = strings.ToLower(strings.TrimSpace(line))
			}
			switch {
			case strings.HasPrefix(choice, "a"):
				if err := refine.Apply(p); err != nil {
					return err
				}
				e, err := a.engine()
				if err != nil {
					return err
				}
				closure, err := e.Loader(repo).Closure(args[0])
				if err != nil {
					return err
				}
				v, err := e.RegisterVersion(repo, closure, c.Store.Dir, history.SourceRefine, p.Summary, p.Addresses())
				if err != nil {
					return err
				}
				fmt.Printf("\nApplied. %s is now %s, addressing %v. New runs record feedback against it.\n", args[0], v.Label(), p.Addresses())
				if repo != "" {
					fmt.Printf("Review and commit the change (and %s/history/%s/) when you're happy.\n", brand.Dir, args[0])
				}
			case strings.HasPrefix(choice, "d"):
				os.Remove(md)
				os.Remove(strings.TrimSuffix(md, ".md") + ".json")
				fmt.Println("Discarded.")
			default:
				fmt.Printf("\nKept the proposal in %s. Edit the pipeline yourself; when you've dealt with feedback that way, close it with `%s pipeline close %s <id>…`.\n", display(md), brand.Name, args[0])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "apply the proposal without asking")
	cmd.Flags().BoolVar(&keep, "keep", false, "save the proposal without applying it")
	cmd.Flags().StringVar(&model, "model", "", "Claude model (default: agent.model from config)")
	cmd.Flags().StringVar(&fake, "fake-agents", "", "")
	cmd.Flags().MarkHidden("fake-agents")
	return cmd
}

func (a *app) showDiff(path, content string) {
	tmp, err := os.CreateTemp("", "proposed-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(content)
	tmp.Close()
	old := path
	if _, err := os.Stat(path); err != nil {
		old = "/dev/null"
	}
	args := []string{"diff", "--no-index", "--no-prefix"}
	if !a.noColor {
		args = append(args, "--color=always")
	}
	out, _ := exec.Command("git", append(args, "--", old, tmp.Name())...).Output()
	lines := strings.Split(string(out), "\n")
	// Skip git's header lines; the file name is printed above.
	for _, l := range lines {
		plain := strings.TrimLeft(l, "\x1b[0123456789;m")
		if strings.HasPrefix(plain, "diff --git") || strings.HasPrefix(plain, "index ") || strings.HasPrefix(plain, "--- ") || strings.HasPrefix(plain, "+++ ") || strings.HasPrefix(plain, "new file mode") {
			continue
		}
		fmt.Println(l)
	}
}

func (a *app) reportCmd() *cobra.Command {
	var model, fake string
	var noSave bool
	cmd := &cobra.Command{
		Use:   "report <pipeline>",
		Short: "Analyse feedback trends across a pipeline's versions",
		Long: `Have Claude read all the feedback on a pipeline, version by version, and
report recurring themes, what changed after each version (improvements and
regressions), and what's most worth addressing next. It analyses your
judgements; it doesn't grade the work itself. Reports are saved under
` + brand.Dir + `/history/<pipeline>/reports/.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := a.refineContext(cmd, args[0], model, fake)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			md, cost, err := refine.Report(ctx, c)
			if errors.Is(err, refine.ErrNotEnough) {
				fmt.Printf("Not enough feedback on %s for a report yet: %v.\n", args[0], err)
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Println(md)
			if !noSave {
				if path, err := refine.SaveReport(c.Store, md); err == nil {
					fmt.Println(a.dim("\nSaved to " + tildify(path)))
				}
			}
			if cost > 0 {
				fmt.Println(a.dim(fmt.Sprintf("(cost %s)", money(cost))))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "Claude model (default: agent.model from config)")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "don't save the report")
	cmd.Flags().StringVar(&fake, "fake-agents", "", "")
	cmd.Flags().MarkHidden("fake-agents")
	return cmd
}
