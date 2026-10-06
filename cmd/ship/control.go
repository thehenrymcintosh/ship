package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/daemon"
	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/engine/steps"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// post sends a control command to the daemon and prints the new status.
func (a *app) post(run, endpoint string, body map[string]any) error {
	_, err := a.postSnap(run, endpoint, body)
	return err
}

// postSnap sends a run command and prints (and returns) the run after it.
func (a *app) postSnap(run, endpoint string, body map[string]any) (*store.RunSnapshot, error) {
	id, err := a.resolveRun(run)
	if err != nil {
		return nil, err
	}
	c, err := daemon.Ensure(a.home)
	if err != nil {
		return nil, err
	}
	var s store.RunSnapshot
	if err := c.Do("POST", "/api/runs/"+id+"/"+endpoint, body, &s); err != nil {
		return nil, err
	}
	if a.json {
		return &s, printJSON(s)
	}
	fmt.Printf("%s  %s  %s\n", shortRef(s.ID), a.status(s.Status), s.CurrentStep)
	return &s, nil
}

func (a *app) answerCmd() *cobra.Command {
	var note, noteFor string
	cmd := &cobra.Command{
		Use:   "answer <run> [choice]",
		Short: "Answer a pending check-in (interactive picker without a choice)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			choice := ""
			if len(args) == 2 {
				choice = args[1]
			} else {
				id, err := a.resolveRun(args[0])
				if err != nil {
					return err
				}
				s, err := a.loadRun(id)
				if err != nil {
					return err
				}
				pa := s.PendingAsk
				if pa == nil {
					return fail(exitConflict, "run %s isn't waiting for an answer (%s)", shortRef(id), s.Status)
				}
				if !isTTY(os.Stdin) {
					return fail(exitUser, "choose one of: %s", strings.Join(pa.Choices, ", "))
				}
				r := bufio.NewReader(os.Stdin)
				card := a.loadCard(id)
				if card != nil {
					a.printCard(os.Stdout, card, id)
				} else {
					fmt.Println(a.bold(pa.Question))
				}
				if pa.Kind == store.AskKindVar {
					fmt.Print("value: ")
					v, _ := r.ReadString('\n')
					note = strings.TrimSpace(v)
				} else {
					rec := ""
					if card != nil {
						rec = card.Recommended
					}
					if card == nil || len(card.Options) == 0 {
						for i, c := range pa.Choices {
							fmt.Printf("  %d) %s\n", i+1, c)
						}
					}
					if rec != "" {
						fmt.Printf("choose (Enter for %s): ", rec)
					} else {
						fmt.Print("choose: ")
					}
					v, _ := r.ReadString('\n')
					choice = strings.TrimSpace(v)
					if choice == "" {
						choice = rec
					}
					if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(pa.Choices) {
						choice = pa.Choices[n-1]
					}
					if note == "" && pa.Input != "none" {
						fmt.Print("note (optional): ")
						v, _ := r.ReadString('\n')
						note = strings.TrimSpace(v)
					}
				}
				if pa.Kind == store.AskKindSplitReview {
					return a.post(args[0], "split-review", map[string]any{"action": choice, "note": note})
				}
			}
			if noteFor != "step" && noteFor != "run" {
				return fail(exitUser, "--for is step or run, not %q", noteFor)
			}
			return a.post(args[0], "answer", map[string]any{"choice": choice, "note": note, "for": noteFor})
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "free-text note: the check-in's handover, which the next step reads")
	cmd.Flags().StringVar(&noteFor, "for", "step", `who the note is for: "step" (the next step) or "run" (every later agent step of the run, and its slices)`)
	return cmd
}

func (a *app) reviewCmd() *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:       "review <run> approve|resplit|reload|stop",
		Short:     "Act on a split review",
		Args:      cobra.ExactArgs(2),
		ValidArgs: []string{"approve", "resplit", "reload", "stop"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.post(args[0], "split-review", map[string]any{"action": args[1], "note": note})
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "note (required for resplit)")
	return cmd
}

func (a *app) simpleCmd(use, short, _ string, endpoint string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <run>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.post(args[0], endpoint, nil)
		},
	}
}

func (a *app) startNowCmd() *cobra.Command {
	var slice int
	cmd := &cobra.Command{
		Use:   "start-now <run> [--slice N]",
		Short: "Start a run waiting on another (--after) now, or a fanout's pending slice",
		Long: `Start a run created with --after straight away, without waiting for the
run it waits on.

With --slice, start that pending slice of a run that's running its slices,
now: it skips the series order and max_parallel. A slice that stacks on the
one before can start once that one has.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if slice > 0 {
				return a.post(args[0], "start-slice", map[string]any{"slice": strconv.Itoa(slice)})
			}
			return a.post(args[0], "start-now", nil)
		},
	}
	cmd.Flags().IntVar(&slice, "slice", 0, "the number of the slice to start")
	return cmd
}

func (a *app) gotoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "goto <run> <step>",
		Short: "Send a run to a step (stops the running step; resets its counter)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.post(args[0], "goto", map[string]any{"step": args[1]})
		},
	}
}

func (a *app) upgradeCmd() *cobra.Command {
	var step string
	cmd := &cobra.Command{
		Use:   "upgrade <run>",
		Short: "Move a run onto its pipeline as it is now and continue at a step",
		Long: `Move a run onto the current version of its pipeline (fixing a pipeline
mid-run, for example) and continue at --step: the current step by default.
A running step is stopped, as with goto, and the run is versioned again.

The run's agents still read skills, rules and scripts from its worktree;
you're warned about any the worktree has an older copy of.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.postSnap(args[0], "upgrade", map[string]any{"step": step})
			if err != nil || a.json {
				return err
			}
			if s.PipelineVersion > 0 {
				fmt.Printf("now on %s v%d\n", s.Pipeline, s.PipelineVersion)
			}
			for _, w := range s.Warnings {
				fmt.Println(a.color("33", "warning: "+w))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&step, "step", "", "step to continue at (default: the current step)")
	return cmd
}

func (a *app) budgetCmd() *cobra.Command {
	var usd, tokens string
	var retry bool
	cmd := &cobra.Command{
		Use:   "budget <run> [--usd N] [--tokens N]",
		Short: "Raise a run's budget (and --retry a run that stopped at it)",
		Long: `Add to a run's budget beyond its pipeline's limits (limits.max_budget_usd and
limits.max_tokens). A run that reaches its budget waits in your inbox; raise
it with --retry to carry on from the step it stopped at.`,
		Example: `  ship budget 3fa --usd 5 --retry
  ship budget 3fa --tokens 500k`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"usd": usd, "tokens": tokens}
			if retry {
				body["action"] = "retry"
			}
			s, err := a.postSnap(args[0], "budget", body)
			if err != nil || a.json {
				return err
			}
			fmt.Println(budgetLine(s))
			return nil
		},
	}
	cmd.Flags().StringVar(&usd, "usd", "", "dollars to add")
	cmd.Flags().StringVar(&tokens, "tokens", "", "tokens to add (e.g. 200k, 1m)")
	cmd.Flags().BoolVar(&retry, "retry", false, "retry the step the run stopped at")
	return cmd
}

// budgetLine says what a run has spent against its budget.
func budgetLine(s *store.RunSnapshot) string {
	line := fmt.Sprintf("spent $%.2f", s.CostUSD)
	if s.Tokens > 0 {
		line += ", " + steps.FormatTokens(s.Tokens) + " tokens"
	}
	return line
}

func (a *app) setCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <run> <var>=<value>",
		Short: "Set a run variable (format is enforced)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, v, ok := strings.Cut(args[1], "=")
			if !ok || k == "" {
				return fail(exitUser, "want <var>=<value>")
			}
			return a.post(args[0], "vars", map[string]any{"name": k, "value": v})
		},
	}
}

func (a *app) cleanCmd() *cobra.Command {
	var run string
	var force, dry bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Release the worktrees of finished runs (ship prune deletes their records)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := daemon.Ensure(a.home)
			if err != nil {
				return err
			}
			var res struct {
				Released []string `json:"released"`
				Errors   []string `json:"errors"`
			}
			if err := c.Do("POST", "/api/clean", map[string]any{"run": run, "force": force, "dry_run": dry}, &res); err != nil {
				return err
			}
			if a.json {
				return printJSON(res)
			}
			verb := ""
			if dry {
				verb = "would be "
			}
			for _, id := range res.Released {
				fmt.Printf("worktree %sreleased  %s\n", verb, id)
			}
			for _, e := range res.Errors {
				fmt.Fprintln(os.Stderr, a.color("31", e))
			}
			if len(res.Released) == 0 && len(res.Errors) == 0 {
				fmt.Println("Nothing to clean.")
			}
			if len(res.Errors) > 0 {
				return fail(exitUser, "some worktrees weren't released (dirty? use --force)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&run, "run", "", "only this run")
	cmd.Flags().BoolVar(&force, "force", false, "release dirty worktrees too")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "show what would happen")
	return cmd
}

func (a *app) pruneCmd() *cobra.Command {
	var olderThan string
	var force, dry bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete the records of old finished runs (retention.keep_runs_days, default 30)",
		Long: `Delete what ship keeps about finished runs (event logs, transcripts,
handovers) once they finished longer ago than --older-than. Slices go with
their run. Active runs are never touched, and runs that still have a
worktree are kept unless --force, which removes the worktree too. Pipeline
history and feedback in the repo are kept.

The default age is retention.keep_runs_days in the config (30); when that
is 0, nothing is pruned without --older-than.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := daemon.Ensure(a.home)
			if err != nil {
				return err
			}
			var res engine.PruneResult
			if err := c.Do("POST", "/api/prune", map[string]any{"older_than": olderThan, "force": force, "dry_run": dry}, &res); err != nil {
				return err
			}
			if a.json {
				return printJSON(res)
			}
			for _, p := range res.Pruned {
				extra := ""
				if p.Slices > 0 {
					extra = fmt.Sprintf(", %d slices", p.Slices)
				}
				if p.Worktree != "" {
					extra += ", worktree " + p.Worktree
				}
				fmt.Printf("%-8s %s  %s (%s, finished %s%s)\n", humanBytes(p.Bytes), p.ID, p.Title, p.Status, p.Finished.Local().Format("2 Jan 2006"), extra)
			}
			for _, s := range res.Skipped {
				fmt.Println(a.color("2", "kept "+s))
			}
			for _, e := range res.Errors {
				fmt.Fprintln(os.Stderr, a.color("31", e))
			}
			switch {
			case res.Note != "":
				fmt.Println(res.Note)
			case len(res.Pruned) == 0:
				fmt.Println("Nothing to prune.")
			case dry:
				fmt.Printf("Would delete %d runs and free %s. Run again without --dry-run to do it.\n", len(res.Pruned), humanBytes(res.Bytes))
			default:
				fmt.Printf("Deleted %d runs and freed %s.\n", len(res.Pruned), humanBytes(res.Bytes))
			}
			if len(res.Errors) > 0 {
				return fail(exitUser, "some runs couldn't be pruned")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "", "only runs finished longer ago than this (e.g. 30d, 12h)")
	cmd.Flags().BoolVar(&force, "force", false, "also prune runs that still have a worktree, removing it")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "list what would be deleted and the space it frees")
	return cmd
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// transcriptLine renders one stream-json line for `logs`.
func transcriptLine(line string) string {
	var l struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message *struct {
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
		Cost    float64 `json:"total_cost_usd"`
		IsError bool    `json:"is_error"`
	}
	if json.Unmarshal([]byte(line), &l) != nil {
		return ""
	}
	var out []string
	switch l.Type {
	case "assistant":
		if l.Message == nil {
			return ""
		}
		for _, c := range l.Message.Content {
			switch c.Type {
			case "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					out = append(out, t)
				}
			case "tool_use":
				in := string(c.Input)
				out = append(out, "⚙ "+c.Name+" "+truncate(in, 160))
			}
		}
	case "result":
		s := "■ finished (" + l.Subtype + ")"
		if l.Cost > 0 {
			s += fmt.Sprintf(" $%.2f", l.Cost)
		}
		out = append(out, s)
	}
	return strings.Join(out, "\n")
}

func (a *app) pauseCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "pause <run>",
		Short: "Pause a run after its current step (a splitting parent stops starting slices)",
		Long: `Pause a run. The step that's running finishes, then the run holds before the
next one; its worktree and agent conversations stay as they are. A run that's
waiting for you pauses once you've answered.

For a parent running slices, no new slices start while it's paused; slices
already running carry on. --all pauses them too. Resume with ` + "`" + `ship resume` + "`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.post(args[0], "pause", map[string]any{"all": all})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also pause the run's running slices")
	return cmd
}

func (a *app) resumeCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "resume <run>",
		Short: "Resume a paused run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.post(args[0], "resume", map[string]any{"all": all})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also resume the run's paused slices")
	return cmd
}

func (a *app) prCmd() *cobra.Command {
	var address bool
	cmd := &cobra.Command{
		Use:   "pr <run>",
		Short: "Show what a run's PR watch sees; --address sends pending review comments now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if address {
				return a.post(args[0], "pr/trigger", nil)
			}
			id, err := a.resolveRun(args[0])
			if err != nil {
				return err
			}
			s, err := a.loadRun(id)
			if err != nil {
				return err
			}
			pr := s.PR
			if pr == nil {
				return fail(exitNotFound, "run %s hasn't watched a PR (its pipeline needs a pr: step)", shortRef(id))
			}
			if a.json {
				return printJSON(pr)
			}
			if pr.URL == "" {
				fmt.Println(pr.Note)
				return nil
			}
			fmt.Printf("PR #%d  %s  %s\n", pr.Number, strings.ToLower(pr.State), pr.URL)
			fmt.Printf("checks: %d passed, %d failed, %d running\n", pr.ChecksPass, pr.ChecksFail, pr.ChecksPending)
			for _, f := range pr.Failing {
				fmt.Printf("  ✗ %s\n", f)
			}
			if pr.PendingComments > 0 {
				when := ""
				if pr.LastCommentAt != nil {
					when = ", latest " + ago(*pr.LastCommentAt)
				}
				mode := fmt.Sprintf("sent once nobody has commented for %s", time.Duration(pr.SettleSeconds)*time.Second)
				if pr.Trigger == "manual" {
					mode = "waiting for you"
				}
				fmt.Printf("%d new review comment(s)%s: %s. Send now with `ship pr %s --address`.\n", pr.PendingComments, when, mode, shortRef(id))
			} else {
				fmt.Println("no new review comments")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&address, "address", false, "send the pending review comments to the pipeline now")
	return cmd
}
