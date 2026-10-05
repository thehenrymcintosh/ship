package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/engine/steps"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// pipelineStats is `ship pipeline stats --json`.
type pipelineStats struct {
	Pipeline string              `json:"pipeline"`
	Runs     []string            `json:"runs"`
	Steps    []store.StepAverage `json:"steps"`
}

func (a *app) statsCmd() *cobra.Command {
	var last int
	var all bool
	cmd := &cobra.Command{
		Use:   "stats <pipeline>",
		Short: "Average time, cost and tokens per step across a pipeline's recent runs",
		Long: `Average each step of a pipeline across its recent finished runs: visits per
run, time, cost and tokens (input, output, cache writes and cache reads).

A step that takes far longer or uses far more tokens than the rest may be doing
too much and be worth splitting; a very small one may be worth merging into its
neighbour. Only runs that finished done count, unless you pass --all.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.store()
			if err != nil {
				return err
			}
			snaps, err := st.List()
			if err != nil {
				return err
			}
			repo := ""
			if f, _ := cmd.Flags().GetString("repo"); f != "" {
				if repo, err = repoRoot(f); err != nil {
					return err
				}
			}
			var runs []*store.RunSnapshot
			for _, s := range snaps {
				if s.Pipeline != args[0] || !s.Status.Terminal() || (!all && s.Status != store.StatusDone) {
					continue
				}
				if repo != "" && s.Repo != repo {
					continue
				}
				runs = append(runs, s)
			}
			finished := func(s *store.RunSnapshot) time.Time {
				if s.FinishedAt != nil {
					return *s.FinishedAt
				}
				return s.UpdatedAt
			}
			sort.Slice(runs, func(i, j int) bool { return finished(runs[i]).After(finished(runs[j])) })
			if last > 0 && len(runs) > last {
				runs = runs[:last]
			}
			out := pipelineStats{Pipeline: args[0], Runs: []string{}, Steps: store.AverageStepStats(runs)}
			for _, s := range runs {
				out.Runs = append(out.Runs, s.ID)
			}
			if out.Steps == nil {
				out.Steps = []store.StepAverage{}
			}
			if a.json {
				return printJSON(out)
			}
			if len(runs) == 0 {
				which := "done"
				if all {
					which = "finished"
				}
				fmt.Printf("No %s runs of %s yet.\n", which, args[0])
				return nil
			}
			fmt.Printf("%s: averages per run over the last %d %s\n\n", a.bold(args[0]), len(runs), plural(len(runs), "run"))
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "STEP\tRUNS\tVISITS\tTIME\tCOST\tTOKENS\tIN\tOUT\tCACHE WRITE\tCACHE READ")
			tok := func(n int64) string {
				if n <= 0 {
					return "-"
				}
				return steps.FormatTokens(n)
			}
			for _, s := range out.Steps {
				dur := "-"
				if s.DurationMS > 0 {
					dur = (time.Duration(s.DurationMS) * time.Millisecond).Round(time.Second).String()
				}
				cost := money(s.CostUSD)
				if cost == "" {
					cost = "-"
				}
				fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Step, s.Runs,
					strings.TrimSuffix(fmt.Sprintf("%.1f", s.Visits), ".0"), dur, cost,
					tok(s.Tokens), tok(s.Usage.Input), tok(s.Usage.Output), tok(s.Usage.CacheWrite), tok(s.Usage.CacheRead))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Println(a.dim("\nRUNS is how many of the runs visited the step; the rest are per run. A step far bigger than the rest may be worth splitting; a tiny one may be worth merging into its neighbour."))
			return nil
		},
	}
	cmd.Flags().IntVar(&last, "last", 20, "how many recent runs to average")
	cmd.Flags().BoolVar(&all, "all", false, "include runs that stopped, failed or were cancelled")
	return cmd
}

func plural(n int, s string) string {
	if n == 1 {
		return s
	}
	return s + "s"
}
