package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/engine/steps"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// pipelineStats is `ship pipeline stats --json`.
type pipelineStats struct {
	Pipeline string              `json:"pipeline"`
	Runs     []string            `json:"runs"`
	Steps    []store.StepAverage `json:"steps"`
	// Per step: the share of runs where it needed a person.
	NeedsPerson map[string]float64 `json:"needs_person"`
	Human       humanSummary       `json:"human"`
	// Unrecorded counts finished runs on this machine with no record yet
	// (see --backfill).
	Unrecorded int `json:"unrecorded,omitempty"`
}

// humanSummary is how hands-on the runs were, per run.
type humanSummary struct {
	HandsOn       float64 `json:"hands_on"`      // check-ins plus interventions
	CheckIns      float64 `json:"check_ins"`     // questions answered
	Interventions float64 `json:"interventions"` // retries, gotos… (not pause/resume)
	WaitMS        int64   `json:"wait_ms"`       // waiting for a person
	Autonomous    float64 `json:"autonomous"`    // share of runs nobody touched before the PR
	PRRounds      float64 `json:"pr_rounds"`     // review batches and CI failures, of runs with a PR
	Feedback      float64 `json:"feedback"`      // feedback recorded against the run
}

func summarizeHuman(runs []history.RunStats) humanSummary {
	var h humanSummary
	n := float64(len(runs))
	if n == 0 {
		return h
	}
	prRuns := 0
	for _, r := range runs {
		h.HandsOn += float64(r.Human.HandsOn())
		h.CheckIns += float64(r.Human.CheckIns)
		h.Interventions += float64(r.Human.Corrective())
		h.WaitMS += r.Human.WaitMS
		h.Feedback += float64(r.Feedback)
		if r.Human.Autonomous {
			h.Autonomous++
		}
		if r.PR != nil {
			prRuns++
			h.PRRounds += float64(r.PR.Rounds)
		}
	}
	h.HandsOn /= n
	h.CheckIns /= n
	h.Interventions /= n
	h.WaitMS = int64(float64(h.WaitMS) / n)
	h.Feedback /= n
	h.Autonomous /= n
	if prRuns > 0 {
		h.PRRounds /= float64(prRuns)
	}
	return h
}

func (a *app) statsCmd() *cobra.Command {
	var last int
	var all, backfill bool
	cmd := &cobra.Command{
		Use:   "stats <pipeline>",
		Short: "Time, cost, tokens and hands-on effort per step across a pipeline's recent runs",
		Long: `Average each step of a pipeline across its recent finished runs: visits per
run, time, cost and tokens (input, output, cache writes and cache reads), and
how often it needed a person. Above the table: how hands-on the runs were
(check-ins answered, interventions such as retries, time spent waiting for
you, PR review rounds); the less, the better the pipeline is doing. A run
is fully autonomous when nobody answered a check-in or stepped in before
its PR; PR review rounds are counted separately.

The numbers come from the run records in the pipeline's history
(runs/<run-id>.json, committed with the pipeline), so they include runs from
everyone who uses it. This command only reads them. Runs that finished on
this machine before records were kept are added with --backfill. Only runs
that finished done count, unless you pass --all. The pipeline page in the
web UI compares versions.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, _, hs, _, err := a.pipelineRead(cmd, args[0])
			if err != nil {
				return err
			}
			unrecorded := 0
			if backfill {
				n, err := e.BackfillStats(hs.Dir, args[0])
				if err != nil {
					return err
				}
				if !a.json {
					fmt.Println(a.dim(fmt.Sprintf("Recorded %d earlier %s from this machine.", n, plural(n, "run"))))
				}
			} else {
				unrecorded = e.UnrecordedRuns(hs.Dir, args[0])
			}
			recorded, err := hs.Runs()
			if err != nil {
				return err
			}
			var runs []history.RunStats
			for _, r := range recorded {
				if r.ParentRun == "" && (all || r.Status == store.StatusDone) {
					runs = append(runs, r)
				}
			}
			sort.SliceStable(runs, func(i, j int) bool { return runs[i].Finished.After(runs[j].Finished) })
			if last > 0 && len(runs) > last {
				runs = runs[:last]
			}
			out := pipelineStats{Pipeline: args[0], Runs: []string{}, Steps: history.AverageSteps(runs), NeedsPerson: map[string]float64{}, Human: summarizeHuman(runs), Unrecorded: unrecorded}
			for _, s := range runs {
				out.Runs = append(out.Runs, s.Run)
				for _, st := range s.Steps {
					if st.Human.Needed() {
						out.NeedsPerson[st.Step] += 1 / float64(len(runs))
					}
				}
			}
			if out.Steps == nil {
				out.Steps = []store.StepAverage{}
			}
			if a.json {
				return printJSON(out)
			}
			if unrecorded > 0 {
				have := "have"
				if unrecorded == 1 {
					have = "has"
				}
				fmt.Println(a.dim(fmt.Sprintf("%d finished %s on this machine %s no record yet: `%s pipeline stats %s --backfill` adds them.",
					unrecorded, plural(unrecorded, "run"), have, brand.Name, args[0])))
			}
			if len(runs) == 0 {
				which := "done"
				if all {
					which = "finished"
				}
				fmt.Printf("No %s runs of %s recorded yet.\n", which, args[0])
				return nil
			}
			fmt.Printf("%s: averages per run over the last %d %s\n\n", a.bold(args[0]), len(runs), plural(len(runs), "run"))
			h := out.Human
			fmt.Printf("Hands-on: %s per run (%s check-ins, %s interventions), %s waiting for a person; %.0f%% were fully autonomous (no check-ins or interventions before the PR).\n",
				num(h.HandsOn), num(h.CheckIns), num(h.Interventions), dur(h.WaitMS), h.Autonomous*100)
			if h.PRRounds > 0 || h.Feedback > 0 {
				fmt.Printf("PR review rounds: %s per run with a PR; feedback: %s per run.\n", num(h.PRRounds), num(h.Feedback))
			}
			fmt.Println()
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "STEP\tRUNS\tVISITS\tTIME\tCOST\tTOKENS\tIN\tOUT\tCACHE WRITE\tCACHE READ\tNEEDS A PERSON")
			tok := func(n int64) string {
				if n <= 0 {
					return "-"
				}
				return steps.FormatTokens(n)
			}
			for _, s := range out.Steps {
				cost := money(s.CostUSD)
				if cost == "" {
					cost = "-"
				}
				person := "-"
				if p := out.NeedsPerson[s.Step]; p > 0 {
					person = fmt.Sprintf("%.0f%%", p*100)
				}
				fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Step, s.Runs,
					num(s.Visits), dur(s.DurationMS), cost,
					tok(s.Tokens), tok(s.Usage.Input), tok(s.Usage.Output), tok(s.Usage.CacheWrite), tok(s.Usage.CacheRead), person)
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
	cmd.Flags().BoolVar(&backfill, "backfill", false, "record finished runs on this machine that have no record yet")
	return cmd
}

func num(f float64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0") }

func dur(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

func plural(n int, s string) string {
	if n == 1 {
		return s
	}
	return s + "s"
}
