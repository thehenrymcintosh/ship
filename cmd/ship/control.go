package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/daemon"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// post sends a control command to the daemon and prints the new status.
func (a *app) post(run, endpoint string, body map[string]string) error {
	id, err := a.resolveRun(run)
	if err != nil {
		return err
	}
	c, err := daemon.Ensure(a.home)
	if err != nil {
		return err
	}
	var s store.RunSnapshot
	if err := c.Do("POST", "/api/runs/"+id+"/"+endpoint, body, &s); err != nil {
		return err
	}
	if a.json {
		return printJSON(s)
	}
	fmt.Printf("%s  %s  %s\n", shortRef(s.ID), a.status(s.Status), s.CurrentStep)
	return nil
}

func (a *app) answerCmd() *cobra.Command {
	var note string
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
				fmt.Println(a.bold(pa.Question))
				if pa.Kind == store.AskKindVar {
					fmt.Print("value: ")
					v, _ := r.ReadString('\n')
					note = strings.TrimSpace(v)
				} else {
					for i, c := range pa.Choices {
						fmt.Printf("  %d) %s\n", i+1, c)
					}
					fmt.Print("choose: ")
					v, _ := r.ReadString('\n')
					choice = strings.TrimSpace(v)
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
					return a.post(args[0], "split-review", map[string]string{"action": choice, "note": note})
				}
			}
			return a.post(args[0], "answer", map[string]string{"choice": choice, "note": note})
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "free-text note (becomes the visit's summary)")
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
			return a.post(args[0], "split-review", map[string]string{"action": args[1], "note": note})
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

func (a *app) gotoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "goto <run> <step>",
		Short: "Send a run to a step (stops the running step; resets its counter)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.post(args[0], "goto", map[string]string{"step": args[1]})
		},
	}
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
			return a.post(args[0], "vars", map[string]string{"name": k, "value": v})
		},
	}
}

func (a *app) cleanCmd() *cobra.Command {
	var run string
	var force, dry bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Release worktrees of finished runs and delete old run dirs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := daemon.Ensure(a.home)
			if err != nil {
				return err
			}
			var res struct {
				Released []string `json:"released"`
				Deleted  []string `json:"deleted"`
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
			for _, id := range res.Deleted {
				fmt.Printf("run dir %sdeleted    %s\n", verb, id)
			}
			for _, e := range res.Errors {
				fmt.Fprintln(os.Stderr, a.color("31", e))
			}
			if len(res.Released)+len(res.Deleted) == 0 && len(res.Errors) == 0 {
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
