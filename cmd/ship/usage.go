package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/daemon"
)

func (a *app) usageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "usage",
		Short: "Show Claude's usage limits and what each run has spent this window",
		Long: `Show where your Claude account stands against its usage limits (as the
claude CLI last reported during an agent step), and what each run has spent
in the current 5-hour window with its estimated share of it.

When the limit is close and several runs are going, focus on one with
` + "`ship focus <run>`" + `: the others pause after their current step, so one finishes
instead of all of them stopping halfway.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := daemon.Ensure(a.home)
			if err != nil {
				return err
			}
			var u daemon.UsageView
			if err := c.Do("GET", "/api/usage", nil, &u); err != nil {
				return err
			}
			if a.json {
				return printJSON(u)
			}
			if !u.Known {
				fmt.Println("No usage reported yet: it appears once an agent step has run.")
				return nil
			}
			fmt.Printf("Claude usage (as of %s; counts all use of your account):\n", u.UpdatedAt.Local().Format("15:04"))
			for _, w := range u.Windows {
				bar := strings.Repeat("█", w.Pct/5) + strings.Repeat("░", 20-min(w.Pct, 100)/5)
				line := fmt.Sprintf("  %-12s %s %3d%%", w.Label, bar, w.Pct)
				if !w.ResetsAt.IsZero() {
					line += "  resets " + w.ResetsAt.Local().Format("Mon 15:04") + " (in " + time.Until(w.ResetsAt).Round(time.Minute).String() + ")"
				}
				color := map[string]string{"warn": "33", "danger": "31"}[w.Level]
				if color != "" {
					line = a.color(color, line)
				}
				fmt.Println(line)
			}
			if u.Rejected {
				fmt.Println(a.color("31", "  The limit has been reached: agent steps wait for it to reset, then carry on."))
			}
			if len(u.Runs) > 0 {
				fmt.Println()
				tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "RUN\tSTATUS\tTHIS WINDOW\t≈ OF LIMIT\tTITLE")
				for _, r := range u.Runs {
					pct := ""
					if r.Pct > 0 {
						pct = fmt.Sprintf("%d%%", r.Pct)
					}
					st := string(r.Status)
					if r.Paused && st != "paused" {
						st += " (pausing)"
					}
					fmt.Fprintf(tw, "%s\t%s\t$%.2f\t%s\t%s\n", shortRef(r.ID), st, r.USD, pct, r.Title)
				}
				tw.Flush()
			}
			if u.Advice != "" {
				fmt.Println()
				fmt.Println(a.color("33", u.Advice) + " (`ship focus <run>`)")
			}
			return nil
		},
	}
}

func (a *app) focusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "focus <run>",
		Short: "Pause every other active run so this one can finish",
		Long: `Pause every other active run (and its slices) after its current step, so
the usage you have left goes to finishing this run. Resume the others later
with ` + "`ship resume <run> --all`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.resolveRun(args[0])
			if err != nil {
				return err
			}
			c, err := daemon.Ensure(a.home)
			if err != nil {
				return err
			}
			var res struct {
				Paused []string `json:"paused"`
				Errors []string `json:"errors,omitempty"`
			}
			if err := c.Do("POST", "/api/runs/"+id+"/focus", nil, &res); err != nil {
				return err
			}
			if a.json {
				return printJSON(res)
			}
			if len(res.Paused) == 0 && len(res.Errors) == 0 {
				fmt.Println("No other runs are active.")
				return nil
			}
			for _, p := range res.Paused {
				fmt.Printf("pausing %s after its current step\n", shortRef(p))
			}
			for _, e := range res.Errors {
				fmt.Fprintln(os.Stderr, a.color("31", "couldn't pause "+e))
			}
			if len(res.Errors) > 0 {
				return fail(exitUser, "some runs couldn't be paused")
			}
			return nil
		},
	}
}
