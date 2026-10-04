package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/brief"
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/daemon"
	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/proc"
	"github.com/thehenrymcintosh/ship/internal/store"
)

func (a *app) startCmd() *cobra.Command {
	var briefPath, repoFlag, fakeAgents string
	var vars []string
	var noOpen, foreground bool
	cmd := &cobra.Command{
		Use:   "start [pipeline] --brief <file|->",
		Short: "Start a run from a brief",
		Long: `Start a run of a pipeline from a brief (markdown with YAML front matter).

The pipeline is the argument, else the brief's "pipeline:", else the only
pipeline available. Pipelines come from the repo's ` + brand.Dir + `/pipelines, then the
global ~/` + brand.Dir + `/pipelines.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if briefPath == "" {
				return fail(exitUser, "--brief is required (a file, or - for stdin)")
			}
			repo, err := repoRoot(repoFlag)
			if err != nil {
				return err
			}
			var data []byte
			fromStdin := briefPath == "-"
			if fromStdin {
				data, err = io.ReadAll(os.Stdin)
			} else {
				data, err = os.ReadFile(briefPath)
			}
			if err != nil {
				return fail(exitUser, "reading brief: %v", err)
			}
			b, err := brief.Parse(data)
			if err != nil {
				return fail(exitUser, "%v", err)
			}
			given := map[string]string{}
			for _, kv := range vars {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fail(exitUser, "--var wants name=value, got %q", kv)
				}
				given[k] = v
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			l := a.loader(repo)
			if name, err = engine.ResolvePipeline(l, name, b); err != nil {
				return fail(exitUser, "%v", err)
			}
			// Prompt for missing from_brief vars when a human is at the terminal.
			if f, _ := l.Load(name); f != nil {
				if err := promptVars(f.Pipeline, b, given); err != nil {
					return err
				}
			}
			if fakeAgents != "" {
				if fakeAgents, err = filepath.Abs(fakeAgents); err != nil {
					return err
				}
			}
			req := engine.StartRequest{Repo: repo, Pipeline: name, Brief: data, Vars: given, FakeAgents: fakeAgents}
			if foreground {
				return a.runForeground(req)
			}
			c, err := daemon.Ensure(a.home)
			if err != nil {
				return err
			}
			var res struct{ ID, URL string }
			body := daemon.StartBody{Repo: repo, Pipeline: name, Brief: string(data), Vars: given, FakeAgents: fakeAgents}
			if err := c.Do("POST", "/api/runs", body, &res); err != nil {
				return startError(err)
			}
			if a.json {
				return printJSON(res)
			}
			fmt.Printf("Started %s\n%s\n", a.bold(res.ID), res.URL)
			cfg, _ := config.Load(a.home, repo)
			if !noOpen && cfg.UI.OpenOnStart {
				openBrowser(c.UIURL("/runs/" + res.ID))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&briefPath, "brief", "", "brief file, or - to read stdin")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "repo path (default: the git repo of the current dir)")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "set a from_brief variable (name=value, repeatable)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "don't open the browser")
	cmd.Flags().BoolVar(&foreground, "foreground", false, "run in this process without the daemon, answering check-ins here")
	cmd.Flags().StringVar(&fakeAgents, "fake-agents", "", "run every agent step on the scripted fake adapter")
	cmd.Flags().MarkHidden("fake-agents")
	return cmd
}

func startError(err error) error {
	var se *daemon.StatusError
	if errors.As(err, &se) && len(se.Body.Errors) > 0 {
		for _, f := range se.Body.Errors {
			fmt.Fprintln(os.Stderr, f)
		}
	}
	return err
}

// tty opens the controlling terminal for prompts (stdin may hold the brief).
func tty() (*os.File, bool) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, false
	}
	return f, true
}

func promptVars(p *pipeline.Pipeline, b *brief.Brief, given map[string]string) error {
	var missing []string
	for _, n := range p.SortedVars() {
		v := p.Variables[n]
		if v == nil || v.Source() != pipeline.SourceFromBrief || v.Prompt == "" {
			continue
		}
		if _, ok := given[n]; ok {
			continue
		}
		if _, ok := b.Vars[n]; ok {
			continue
		}
		missing = append(missing, n)
	}
	if len(missing) == 0 {
		return nil
	}
	t, ok := tty()
	if !ok || !isTTY(os.Stdout) {
		return nil // the engine reports what's missing
	}
	defer t.Close()
	r := bufio.NewReader(t)
	for _, n := range missing {
		v := p.Variables[n]
		for {
			fmt.Fprintf(t, "%s (%s): ", v.Prompt, n)
			line, err := r.ReadString('\n')
			if err != nil {
				return fail(exitUser, "no value for %s", n)
			}
			line = strings.TrimSpace(line)
			if err := engine.CheckFormat(p, n, line); err != nil {
				fmt.Fprintln(t, "  ", err)
				continue
			}
			given[n] = line
			break
		}
	}
	return nil
}

func openBrowser(url string) {
	cmd := exec.Command("open", url)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

// --- foreground ----------------------------------------------------------

func (a *app) runForeground(req engine.StartRequest) error {
	if daemon.Running(a.home) {
		return fail(exitConflict, "a %s daemon is running; it owns run state. Drop --foreground, or stop it with `%s serve --restart`… (or kill it) first", brand.Name, brand.Name)
	}
	p := &printer{a: a, prompts: make(chan prompt, 16)}
	opts, err := daemon.EngineOptions(a.home, p, proc.LoginEnv(context.Background()), true)
	if err != nil {
		return err
	}
	e := engine.New(opts)
	p.e = e
	snap, err := e.Start(context.Background(), req)
	if err != nil {
		var ee *engine.Error
		if errors.As(err, &ee) {
			for _, f := range ee.Findings {
				fmt.Fprintln(os.Stderr, f)
			}
		}
		return err
	}
	p.root = snap.ID
	fmt.Printf("%s %s (foreground; Ctrl-C to stop)\n", a.bold("Started"), snap.ID)
	go p.answerLoop()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	select {
	case <-e.Done(snap.ID):
	case <-sigs:
		fmt.Println("\nstopping…")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		e.Shutdown(ctx)
		cancel()
		fmt.Printf("The run is paused. `%s serve` (or any daemon command) will pick it up.\n", brand.Name)
		return &exitError{exitUser, errors.New("interrupted")}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	e.Shutdown(ctx)
	cancel()
	final, _ := e.Snapshot(snap.ID)
	if final != nil && final.Status != store.StatusDone {
		return &exitError{exitUser, fmt.Errorf("run ended %s %s", final.Status, final.StatusReason)}
	}
	return nil
}

type prompt struct {
	run  string
	snap *store.RunSnapshot
}

// printer prints engine activity and queues human prompts.
type printer struct {
	a       *app
	e       *engine.Engine
	root    string
	mu      sync.Mutex
	prompts chan prompt
}

func (p *printer) tag(id string) string {
	if id == p.root || p.root == "" {
		return ""
	}
	return p.a.dim("[" + strings.TrimPrefix(id, p.root+".") + "] ")
}

func (p *printer) RunEvent(snap *store.RunSnapshot, e store.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.tag(snap.ID)
	switch e.Type {
	case store.EvVisitStarted:
		if v := snap.LastVisit(); v != nil {
			q := ""
			if v.Queued {
				q = p.a.dim(" (waiting for a free agent)")
			}
			fmt.Printf("%s%s %s %s%s\n", t, p.a.bold("▸"), pipeline.TypeGlyph(v.Type), p.a.bold(v.Step), q)
		}
	case store.EvVisitFinished:
		for i := len(snap.Visits) - 1; i >= 0; i-- {
			v := snap.Visits[i]
			if v.Finished != nil {
				line := firstLine(v.Summary)
				fmt.Printf("%s  → %s  %s\n", t, p.a.bold(v.Outcome), p.a.dim(truncate(line, 120)))
				break
			}
		}
	case store.EvAskPending:
		p.queue(snap)
	case store.EvStatusChanged:
		if snap.Status == store.StatusNeedsAttention {
			fmt.Printf("%s%s %s\n", t, p.a.status(snap.Status), snap.StatusReason)
			p.queue(snap)
		}
	case store.EvChildStarted:
		fmt.Printf("%s%s child run started\n", t, p.a.dim("☰"))
	case store.EvRunFinished:
		fmt.Printf("%s%s %s\n", t, p.a.status(snap.Status), snap.StatusReason)
	}
}

// queue hands a prompt to the answer loop without blocking the engine.
func (p *printer) queue(snap *store.RunSnapshot) {
	go func() { p.prompts <- prompt{run: snap.ID, snap: snap} }()
}

func (p *printer) Output(runID string, seq int, stream string, chunk []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if stream == "stderr" {
		os.Stderr.Write(chunk)
	} else {
		os.Stdout.Write(chunk)
	}
}

func (p *printer) Agent(runID string, seq int, ev agent.UIEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m, _ := ev.Data.(map[string]any)
	switch ev.Kind {
	case "text":
		if s, _ := m["text"].(string); s != "" {
			fmt.Printf("%s  %s\n", p.tag(runID), truncate(firstLine(s), 160))
		}
	case "tool_use":
		fmt.Printf("%s  %s %v\n", p.tag(runID), p.a.dim("⚙"), m["name"])
	}
}

func (p *printer) answerLoop() {
	in, ok := tty()
	if !ok {
		return
	}
	r := bufio.NewReader(in)
	ask := func(q string) string {
		fmt.Fprint(in, q)
		s, _ := r.ReadString('\n')
		return strings.TrimSpace(s)
	}
	for pr := range p.prompts {
		s, err := p.e.Snapshot(pr.run)
		if err != nil {
			continue
		}
		t := p.tag(pr.run)
		switch {
		case s.Status == store.StatusAsking && s.PendingAsk != nil:
			pa := s.PendingAsk
			fmt.Fprintf(in, "\n%s%s %s\n", t, p.a.color("35", "●"), p.a.bold(pa.Question))
			if pa.Kind == store.AskKindSplitReview {
				for _, sl := range s.ProposedSlices {
					fmt.Fprintf(in, "    %d. %s (%s)  %s\n", sl.Number, sl.Title, sl.Key, p.a.dim(sl.File))
				}
			}
			for {
				var c engine.Command
				if pa.Kind == store.AskKindVar {
					c = engine.Command{Name: engine.CmdAnswer, Note: ask("  value: ")}
				} else {
					for i, ch := range pa.Choices {
						fmt.Fprintf(in, "  %d) %s\n", i+1, ch)
					}
					choice := ask("  choose: ")
					if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(pa.Choices) {
						choice = pa.Choices[n-1]
					}
					note := ""
					if pa.Input != "none" {
						note = ask("  note (optional): ")
					}
					c = engine.Command{Name: engine.CmdAnswer, Choice: choice, Note: note, Source: "cli"}
					if pa.Kind == store.AskKindSplitReview {
						c = engine.Command{Name: engine.CmdSplitReview, Action: choice, Note: note, Source: "cli"}
					}
				}
				if err := p.e.Do(pr.run, c); err != nil {
					fmt.Fprintf(in, "  %v\n", err)
					continue
				}
				break
			}
		case s.Status == store.StatusNeedsAttention:
			for {
				a := ask(fmt.Sprintf("%s  [r]etry, [g]oto <step>, re[s]ume session, [c]ancel: ", t))
				var c engine.Command
				switch {
				case a == "r":
					c = engine.Command{Name: engine.CmdRetry, Source: "cli"}
				case strings.HasPrefix(a, "g "):
					c = engine.Command{Name: engine.CmdGoto, Step: strings.TrimSpace(a[2:]), Source: "cli"}
				case a == "s":
					c = engine.Command{Name: engine.CmdResumeSession, Source: "cli"}
				case a == "c":
					c = engine.Command{Name: engine.CmdCancel, Source: "cli"}
				default:
					continue
				}
				if err := p.e.Do(pr.run, c); err != nil {
					fmt.Fprintf(in, "  %v\n", err)
					continue
				}
				break
			}
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
