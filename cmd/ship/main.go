// Command ship defines, runs and watches local agentic-coding pipelines.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/daemon"
	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/store"
	gitws "github.com/thehenrymcintosh/ship/internal/workspace/git"
)

// Exit codes.
const (
	exitOK          = 0
	exitUser        = 1
	exitUnreachable = 2
	exitNotFound    = 3
	exitConflict    = 4
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func fail(code int, format string, a ...any) error {
	return &exitError{code, fmt.Errorf(format, a...)}
}

// app holds global flags.
type app struct {
	home    string
	json    bool
	noColor bool
}

func main() {
	a := &app{}
	root := &cobra.Command{
		Use:           brand.Name,
		Short:         "Define, run and watch local agentic-coding pipelines",
		Long:          brand.Name + " runs pipelines of agent, script, human and polling steps as durable state machines in isolated git worktrees.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if a.home == "" {
				a.home = config.Home()
			}
			a.home, _ = filepath.Abs(a.home)
			if os.Getenv("NO_COLOR") != "" || !isTTY(os.Stdout) {
				a.noColor = true
			}
		},
	}
	root.PersistentFlags().StringVar(&a.home, "home", "", "state and config dir (default ~/"+brand.Dir+", env "+brand.HomeEnv+")")
	root.PersistentFlags().BoolVar(&a.json, "json", false, "machine-readable output for read commands")
	root.PersistentFlags().BoolVar(&a.noColor, "no-color", false, "disable colour")

	root.AddCommand(
		a.initCmd(), a.validateCmd(), a.schemaCmd(), a.graphCmd(),
		a.startCmd(), a.lsCmd(), a.statusCmd(), a.logsCmd(),
		a.answerCmd(), a.reviewCmd(),
		a.simpleCmd("retry", "Retry the current step (needs attention, or a pending check-in)", engine.CmdRetry, "retry"),
		a.gotoCmd(),
		a.simpleCmd("cancel", "Cancel a run (and its children)", engine.CmdCancel, "cancel"),
		a.simpleCmd("resume-session", "Resume an interrupted agent session", engine.CmdResumeSession, "resume-session"),
		a.setCmd(), a.openCmd(), a.cdCmd(), a.cleanCmd(), a.serveCmd(), a.versionCmd(),
	)
	err := root.Execute()
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(exitCode(err))
}

func exitCode(err error) int {
	var ee *exitError
	var se *daemon.StatusError
	var amb *store.AmbiguousError
	var engErr *engine.Error
	switch {
	case errors.As(err, &ee):
		return ee.code
	case errors.Is(err, daemon.ErrUnreachable):
		return exitUnreachable
	case errors.As(err, &se):
		switch se.Status {
		case 404:
			return exitNotFound
		case 409:
			return exitConflict
		}
		return exitUser
	case errors.As(err, &amb):
		return exitUser
	case errors.Is(err, store.ErrNotFound):
		return exitNotFound
	case errors.As(err, &engErr):
		switch engErr.Kind {
		case engine.KindNotFound:
			return exitNotFound
		case engine.KindConflict:
			return exitConflict
		}
	}
	return exitUser
}

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func (a *app) store() (*store.Store, error) { return store.New(filepath.Join(a.home, "state")) }

// resolveRun finds a run by id, unique substring, or suffix.
func (a *app) resolveRun(q string) (string, error) {
	st, err := a.store()
	if err != nil {
		return "", err
	}
	id, err := st.Resolve(q)
	var amb *store.AmbiguousError
	if errors.As(err, &amb) {
		return "", fail(exitUser, "%q matches several runs:\n  %s", q, strings.Join(amb.Matches, "\n  "))
	}
	if err != nil {
		return "", fail(exitNotFound, "no run matches %q", q)
	}
	return id, nil
}

// loadRun returns the latest snapshot (live from the daemon when it runs).
func (a *app) loadRun(id string) (*store.RunSnapshot, error) {
	if c, err := daemon.Connect(a.home); err == nil {
		var s store.RunSnapshot
		if err := c.Do("GET", "/api/runs/"+id, nil, &s); err == nil {
			return &s, nil
		}
	}
	st, err := a.store()
	if err != nil {
		return nil, err
	}
	return st.Load(id)
}

// repoRoot resolves the main checkout of the repo ( step 1).
func repoRoot(flag string) (string, error) {
	dir := flag
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	repo, err := gitws.MainCheckout(context.Background(), abs)
	if err != nil {
		return "", fail(exitUser, "%s is not inside a git repository (use --repo)", abs)
	}
	if r, err := filepath.EvalSymlinks(repo); err == nil {
		repo = r
	}
	return repo, nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ANSI colours for statuses.
func (a *app) color(code, s string) string {
	if a.noColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (a *app) status(s store.Status) string {
	codes := map[store.Status]string{
		store.StatusRunning: "34", store.StatusWaiting: "33", store.StatusAsking: "35",
		store.StatusNeedsAttention: "31;1", store.StatusDone: "32", store.StatusFailed: "31",
		store.StatusFannedOut: "36", store.StatusStarting: "90", store.StatusStopped: "90", store.StatusCancelled: "90",
	}
	return a.color(codes[s], string(s))
}

func (a *app) dim(s string) string  { return a.color("90", s) }
func (a *app) bold(s string) string { return a.color("1", s) }
