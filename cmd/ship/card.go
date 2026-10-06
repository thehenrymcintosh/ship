package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/checkin"
	"github.com/thehenrymcintosh/ship/internal/daemon"
)

// loadCard returns a run's check-in card from the daemon, or nil (not
// waiting for anyone, or no daemon to ask).
func (a *app) loadCard(id string) *daemon.CardView {
	c, err := daemon.Connect(a.home)
	if err != nil {
		return nil
	}
	var card *daemon.CardView
	if err := c.Do("GET", "/api/runs/"+id+"/card", nil, &card); err != nil {
		return nil
	}
	return card
}

// printCard writes a check-in card the way the web UI lays it out:
// kind and where it came from, headline, recommendation, situation,
// options, then how to answer.
func (a *app) printCard(w io.Writer, c *daemon.CardView, run string) {
	code := "34;1" // a decision: blue
	if c.Kind == checkin.Problem {
		code = "33;1" // something broke: amber
	}
	meta := []string{c.Pipeline}
	if c.From != "" {
		meta = append(meta, "from "+c.From)
	}
	if !c.Since.IsZero() {
		meta = append(meta, "waiting "+strings.TrimSuffix(ago(c.Since), " ago"))
	}
	meta = append(meta, firstNonEmpty(money(c.CostUSD), "$0")+" so far")
	fmt.Fprintf(w, "%s  %s\n", a.color(code, "▌"+strings.ToUpper(c.Label)), a.dim(strings.Join(meta, " · ")))
	fmt.Fprintf(w, "%s\n", a.bold(c.Headline))
	if c.Question != "" {
		fmt.Fprintf(w, "%s %s\n", a.dim("asked:"), c.Question)
	}
	// The recommendation first, then what it rests on.
	switch {
	case c.Fix != nil:
		fmt.Fprintf(w, "%s %s\n", a.color("32;1", "→ recommended:"), c.Fix.Label)
		if c.FixWrite != "" {
			fmt.Fprintf(w, "  %s\n  %s\n", a.dim("writes to "+c.FixFile+":"), strings.ReplaceAll(c.FixWrite, "\n", "\n  "))
		}
		if c.FixErr != "" {
			fmt.Fprintf(w, "  %s\n", a.color("33", "can't do it from here: "+c.FixErr))
		}
	case c.Recommended != "":
		reason := ""
		if c.Reason != "" {
			reason = ": " + c.Reason
		}
		fmt.Fprintf(w, "%s %s%s\n", a.color("32;1", "→ recommended:"), a.bold(c.Recommended), reason)
	}
	if c.Situation != "" {
		fmt.Fprintf(w, "%s\n", c.Situation)
	}
	for _, t := range c.Tests {
		fmt.Fprintf(w, "  %s %s\n", a.color("31", "✗"), t)
	}
	if !c.ResetsAt.IsZero() && time.Until(c.ResetsAt) > 0 {
		fmt.Fprintf(w, "%s %s\n", a.dim("limit resets:"), c.ResetsAt.Local().Format("Mon 15:04"))
	}
	if c.SummaryModel != "" {
		fmt.Fprintf(w, "%s\n", a.dim("(summary written by "+c.SummaryModel+" from "+c.From+"'s handover; the full handover is on the run's page: "+brand.Name+" open "+shortRef(run)+")"))
	}
	if len(c.Options) > 0 {
		fmt.Fprintln(w, a.dim("options:"))
		for i, o := range c.Options {
			mark := " "
			label := o.Label
			if o.Recommended {
				mark, label = a.color("32;1", "★"), a.bold(o.Label)
			}
			line := fmt.Sprintf("  %s %d) %s", mark, i+1, label)
			if o.Target != "" {
				line += "  " + a.dim(o.Target)
			}
			if o.Cost != "" {
				line += a.dim(" · " + o.Cost)
			}
			fmt.Fprintln(w, line)
			if o.Consequence != "" {
				fmt.Fprintf(w, "       %s\n", o.Consequence)
			}
		}
	}
	switch {
	case c.Fix != nil && c.Fix.Kind == checkin.FixBudget:
		fmt.Fprintf(w, "%s %s budget %s --usd N --retry\n", a.dim("do it:"), brand.Name, shortRef(run))
	case c.Fix != nil && c.Fix.Kind == checkin.FixRetry:
		fmt.Fprintf(w, "%s %s retry %s\n", a.dim("do it:"), brand.Name, shortRef(run))
	case c.Fix != nil:
		fmt.Fprintf(w, "%s %s open %s (the fix is a button on the run's page)\n", a.dim("do it:"), brand.Name, shortRef(run))
	}
	if len(c.Options) > 0 {
		fmt.Fprintf(w, "%s %s answer %s <choice> [--note … [--for run]]\n", a.dim("answer:"), brand.Name, shortRef(run))
	}
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
