package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/agent/fake"
	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/brief"
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/engine/steps"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/proc"
	"github.com/thehenrymcintosh/ship/internal/store"
	"github.com/thehenrymcintosh/ship/internal/tmpl"
	"github.com/thehenrymcintosh/ship/internal/workspace"
	gitws "github.com/thehenrymcintosh/ship/internal/workspace/git"
)

// runner owns one run: its goroutine is the only writer of the run's state.
type runner struct {
	e    *Engine
	id   string
	dir  string
	log  *store.RunLog
	lock *store.Lock

	pipe     *pipeline.Pipeline
	cfg      config.Config
	provider workspace.Provider
	brief    *brief.Brief

	ctx    context.Context // ends at engine shutdown
	cancel context.CancelFunc

	cmds      chan Command
	visitCmds chan steps.Command
	// sliceCmds hands start_slice commands to a running fanout supervisor.
	sliceCmds chan Command
	awaiting  atomic.Bool
	executing atomic.Bool
	done      chan struct{}

	halted     bool
	recovering bool
	// paused is set by a pause request; the run holds at the next step
	// boundary (a fanout parent stops starting slices straight away).
	paused atomic.Bool
	// wake nudges a waiting fanout supervisor (after resume).
	wake chan struct{}
	next nextVisit
}

// nextVisit carries session handling into the next visit.
type nextVisit struct {
	resumeKind string // interrupted | resplit
	resumeID   string
	note       string
}

func (e *Engine) newRunner(id string, rl *store.RunLog, lock *store.Lock) *runner {
	ctx, cancel := context.WithCancel(e.ctx)
	r := &runner{
		e: e, id: id, dir: rl.Dir(), log: rl, lock: lock, ctx: ctx, cancel: cancel,
		cmds: make(chan Command, 16), visitCmds: make(chan steps.Command, 1), sliceCmds: make(chan Command, 1), done: make(chan struct{}), wake: make(chan struct{}, 1),
	}
	rl.OnEvent = func(ev store.Event, snap *store.RunSnapshot) { e.dispatch(id, ev, snap) }
	return r
}

// load reads the run's pipeline snapshot, brief, config and provider.
func (r *runner) load() error {
	snap := r.log.Snapshot()
	r.paused.Store(snap.PauseRequested)
	f, findings := pipeline.NewLoader(filepath.Join(r.dir, store.PipelineDir)).Load(snap.Pipeline)
	if f == nil {
		msg := "pipeline snapshot missing"
		if len(findings) > 0 {
			msg = findings[0].String()
		}
		return errors.New(msg)
	}
	r.pipe = f.Pipeline
	data, err := os.ReadFile(filepath.Join(r.dir, store.BriefFile))
	if err != nil {
		return err
	}
	if r.brief, err = brief.Parse(data); err != nil {
		return err
	}
	if r.cfg, err = r.e.o.LoadConfig(snap.Repo); err != nil {
		r.e.o.Log.Warn("config", "run", r.id, "err", err)
		r.cfg = config.Defaults()
	}
	r.provider, err = r.e.o.Providers(snap.Provider, r.cfg)
	return err
}

func (r *runner) snap() *store.RunSnapshot { return r.log.Snapshot() }

func (r *runner) emit(typ string, data any) error {
	_, err := r.log.Emit(typ, store.ActorEngine, data)
	if err != nil {
		r.e.o.Log.Error("emit failed", "run", r.id, "type", typ, "err", err)
	}
	return err
}

func (r *runner) emitUser(c Command) {
	args := map[string]string{}
	for k, v := range map[string]string{"choice": c.Choice, "note": c.Note, "for": c.For, "action": c.Action, "step": c.Step, "var": c.Var, "value": c.Value} {
		if v != "" {
			args[k] = v
		}
	}
	if c.Slice > 0 {
		args["slice"] = strconv.Itoa(c.Slice)
	}
	_, _ = r.log.Emit(store.EvCommand, store.ActorUser, store.Command{Name: c.Name, Args: args, Actor: store.ActorUser, Source: c.Source})
}

func (r *runner) setStatus(to store.Status, reason string) error {
	s := r.snap()
	if s.Status == to && s.StatusReason == reason {
		return nil
	}
	return r.emit(store.EvStatusChanged, store.StatusChanged{From: s.Status, To: to, Reason: reason})
}

func reply(c Command, err error) {
	if c.reply != nil {
		select {
		case c.reply <- err:
		default:
		}
	}
}

// loop is the run goroutine.
func (r *runner) loop() {
	defer func() {
		r.log.Close()
		r.lock.Unlock()
		r.cancel()
		close(r.done)
	}()
	if r.recovering {
		if !r.recover() {
			return
		}
	}
	for {
		if r.ctx.Err() != nil {
			return
		}
		s := r.snap()
		if s.Status.Terminal() {
			return
		}
		if s.WaitingOn != "" {
			if !r.waitAfter() {
				return
			}
			continue
		}
		switch s.Status {
		case store.StatusStarting:
			r.acquire()
			continue
		case store.StatusNeedsAttention:
			if !r.parked() {
				return
			}
			continue
		}
		if r.paused.Load() {
			if !r.holdPaused() {
				return
			}
			continue
		}
		if !r.step() {
			return
		}
	}
}

// --- workspace ---------------------------------------------------------------

// reuseProvider is the lease of a child that works in its parent's tree.
type reuseProvider struct{}

func (reuseProvider) Name() string                        { return parentProvider }
func (reuseProvider) Check(context.Context, string) error { return nil }
func (reuseProvider) Exists(l workspace.Lease) bool       { _, err := os.Stat(l.Path); return err == nil }
func (reuseProvider) Release(context.Context, workspace.Lease, workspace.ReleaseOpts) error {
	return nil
}
func (reuseProvider) Acquire(context.Context, workspace.AcquireRequest) (workspace.Lease, error) {
	return workspace.Lease{}, errors.New("reuse: the parent's worktree is inherited, not acquired")
}

// acquire gets the run's workspace and moves to running, or parks.
func (r *runner) acquire() {
	s := r.snap()
	var lease workspace.Lease
	var err error
	if s.Provider == parentProvider {
		var ps *store.RunSnapshot
		if ps, err = r.e.Snapshot(s.Parent.ID); err == nil {
			if ps.Workspace == nil {
				err = errors.New("the parent run has no worktree to reuse")
			} else {
				lease = *ps.Workspace
				lease.Provider = parentProvider
			}
		}
	} else {
		ctx, cancel := context.WithTimeout(r.ctx, 15*time.Minute)
		lease, err = r.provider.Acquire(ctx, workspace.AcquireRequest{Repo: s.Repo, RunID: s.ID, Branch: s.Branch, Base: s.Base, Env: r.e.o.BaseEnv})
		cancel()
	}
	if r.ctx.Err() != nil {
		return
	}
	if err != nil {
		r.park("workspace: " + err.Error())
		return
	}
	if lease.Base == "" {
		lease.Base = s.Base
	}
	r.emit(store.EvWorkspaceAcquired, store.WorkspaceAcquired{Lease: lease})
	r.versionPipeline()
	r.setStatus(store.StatusRunning, "")
}

// versionPipeline records the run's pipeline version: the run's own copy of
// the pipeline, plus the skills, rules and scripts as its worktree has them
// (and the user's skills). That's exactly what its agents will use, so
// feedback on the run lands on the right version.
func (r *runner) versionPipeline() {
	s := r.snap()
	if s.HistoryDir == "" || s.PipelineVersion > 0 {
		return
	}
	closure, err := pipeline.NewLoader(filepath.Join(r.dir, store.PipelineDir)).Closure(s.Pipeline)
	if err != nil {
		r.e.o.Log.Warn("versioning pipeline", "run", r.id, "err", err)
		return
	}
	root := s.Repo
	if s.Workspace != nil {
		root = s.Workspace.Path
	}
	hs := r.e.HistoryStore(RunHistoryDir(s, r.e.Home()))
	hs.Author = GitAuthor(s.Repo)
	v, _, err := hs.Register(history.Compute(history.Inputs{
		Pipelines: closure, Repo: root, Home: r.e.Home(), ClaudeDir: r.e.o.ClaudeDir, Folders: runFolders(s, root),
	}), history.SourceEdit, "", nil)
	if err != nil {
		r.e.o.Log.Warn("versioning pipeline", "run", r.id, "err", err)
		return
	}
	r.emit(store.EvVersioned, store.Versioned{Version: v.Version, Hash: v.Hash})
}

func (r *runner) release(force bool) {
	s := r.snap()
	if s.Workspace == nil || s.Provider == parentProvider || s.Provider == "none" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := r.provider.Release(ctx, *s.Workspace, workspace.ReleaseOpts{Force: force}); err != nil {
		r.e.o.Log.Warn("release workspace", "run", r.id, "err", err)
		return
	}
	r.emit(store.EvWorkspaceReleased, store.WorkspaceReleased{Forced: force})
}

// --- the step loop -------------------------------------------------------

// step runs one iteration for the current step. It returns false when the
// goroutine should exit.
func (r *runner) step() bool {
	s := r.snap()
	name := s.CurrentStep
	switch name {
	case pipeline.TargetDone:
		r.finish(store.StatusDone, "")
		return false
	case pipeline.TargetStop:
		r.finish(store.StatusStopped, "reached stop")
		return false
	}
	if s.Transitions >= r.pipe.MaxTransitions() {
		r.finish(store.StatusFailed, "max_transitions")
		return false
	}
	st := r.pipe.Steps[name]
	if st == nil {
		r.park(fmt.Sprintf("unknown step %q", name))
		return true
	}
	if r.halted {
		r.park("halted by parent: a sibling slice stopped")
		return true
	}
	if max := r.pipe.MaxVisits(st); max > 0 && s.VisitCounts[name] >= max {
		t := r.pipe.WhenExhausted(st)
		if t == pipeline.TargetCameFrom {
			t = s.CameFrom
		}
		if t == "" {
			r.park(fmt.Sprintf("exhausted: step %q reached max_visits (%d)", name, max))
			return true
		}
		r.transition(name, t, "", store.ReasonExhausted, false)
		return true
	}
	if ok, alive := r.askVars(st); !ok {
		return alive
	}
	return r.visit(name, st, nil)
}

// executor returns the executor for a step type.
func (r *runner) executor(typ string) steps.Executor {
	switch typ {
	case pipeline.TypeRun:
		return steps.Run{}
	case pipeline.TypeAsk:
		return steps.Ask{}
	case pipeline.TypeWait:
		return steps.Wait{}
	case pipeline.TypeAgent:
		return steps.Agent{}
	case pipeline.TypeSplit:
		return steps.Split{}
	case pipeline.TypeFanout:
		return &fanoutExec{r: r}
	case pipeline.TypePR:
		return steps.PR{}
	}
	return nil
}

// sessionPlan is how a new agent visit's Claude session is chosen.
type sessionPlan struct {
	sessionID string              // fresh session to start
	resumeID  string              // session to resume
	kind      string              // "", continue, shared, interrupted, resplit, resplit-fresh
	thread    string              // the conversation the visit belongs to
	last      *store.VisitSummary // the conversation's previous visit, when resuming it
}

// sessions decides the agent session for a new visit. Steps whose
// `session:` names the same conversation resume its latest session.
func (r *runner) sessions(s *store.RunSnapshot, name string, st *pipeline.Step, nv nextVisit) sessionPlan {
	if !st.IsAgentLike() {
		return sessionPlan{}
	}
	thread := st.Thread(name)
	switch nv.resumeKind {
	case "interrupted":
		return sessionPlan{resumeID: nv.resumeID, kind: "interrupted", thread: thread}
	case "resplit":
		if nv.resumeID != "" {
			return sessionPlan{resumeID: nv.resumeID, kind: "resplit", thread: thread}
		}
		return sessionPlan{sessionID: uuid.NewString(), kind: "resplit-fresh", thread: thread}
	}
	if thread != "" {
		for i := len(s.Visits) - 1; i >= 0; i-- {
			v := &s.Visits[i]
			// Runs from before named sessions recorded no thread; match continue by step.
			same := v.Thread == thread || v.Thread == "" && st.Session == "continue" && v.Step == name
			if same && v.SessionID != "" {
				kind := "continue"
				if v.Step != name {
					kind = "shared"
				}
				return sessionPlan{resumeID: v.SessionID, kind: kind, thread: thread, last: v}
			}
		}
	}
	return sessionPlan{sessionID: uuid.NewString(), thread: thread}
}

// sinceLastTurn tells a resumed agent what happened since its previous
// visit: the steps that ran in between, and what changed in the worktree.
// runNotes collects the check-in notes meant for the rest of the run: its
// parent's (and theirs, for nested slices) first, then its own.
func (r *runner) runNotes(s *store.RunSnapshot) []agent.RunNote {
	var out []agent.RunNote
	for _, n := range r.e.AncestorNotes(s) {
		out = append(out, agent.RunNote{Step: n.Step, Run: n.Run, Note: n.Note})
	}
	for _, n := range s.RunNotes {
		out = append(out, agent.RunNote{Step: n.Step, Note: n.Note})
	}
	return out
}

// AncestorNote is a note for the rest of the run written on one of a
// slice's ancestor runs.
type AncestorNote struct {
	store.RunNote
	Run string
}

// AncestorNotes returns the notes for the rest of the run written on s's
// ancestors (up to 8 levels up), the furthest ancestor's first. A slice's
// agents get these as well as its own.
func (e *Engine) AncestorNotes(s *store.RunSnapshot) []AncestorNote {
	var out []AncestorNote
	for p, depth := s.Parent, 0; p != nil && depth < 8; depth++ {
		ps, err := e.o.Store.Load(p.ID)
		if err != nil {
			break
		}
		var theirs []AncestorNote
		for _, n := range ps.RunNotes {
			theirs = append(theirs, AncestorNote{RunNote: n, Run: ps.ID})
		}
		out = append(theirs, out...)
		p = ps.Parent
	}
	return out
}

func (r *runner) sinceLastTurn(s *store.RunSnapshot, last *store.VisitSummary, worktree string) string {
	if last == nil {
		return ""
	}
	var b strings.Builder
	var done []string
	for _, v := range s.Visits {
		if v.Seq > last.Seq && v.Finished != nil {
			line := fmt.Sprintf("- %s → %s", v.Step, v.Outcome)
			if sum := firstLine(v.Summary); sum != "" {
				line += ": " + sum
			}
			done = append(done, line)
		}
	}
	if len(done) > 0 {
		b.WriteString("Steps since then:\n" + strings.Join(done, "\n") + "\n")
	} else {
		b.WriteString("No other steps have run since then.\n")
	}
	if last.HeadSHA != "" && worktree != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if log, err := gitws.Git(ctx, worktree, "log", "--oneline", "-n", "30", last.HeadSHA+"..HEAD"); err == nil && log != "" {
			b.WriteString("\nCommits since then:\n" + log + "\n")
		}
		if stat, err := gitws.Git(ctx, worktree, "diff", "--stat", last.HeadSHA); err == nil && stat != "" {
			lines := strings.Split(stat, "\n")
			if len(lines) > 60 {
				lines = append(lines[:59], fmt.Sprintf("… and %d more lines", len(lines)-59))
			}
			b.WriteString("\nFiles changed since then (committed and not):\n" + strings.Join(lines, "\n") + "\n")
			b.WriteString("Run `git diff " + last.HeadSHA[:min(12, len(last.HeadSHA))] + "` to see the changes in full.\n")
		}
	}
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type visitResult struct {
	res steps.Result
	err error
}

type abortReq struct {
	cancel bool
	to     string
	cmd    Command
}

// visit runs one visit of step name. resume re-enters an unfinished visit
// after a restart. It returns false when the goroutine should exit.
func (r *runner) visit(name string, st *pipeline.Step, resume *store.VisitSummary) bool {
	s := r.snap()
	typ := st.Type()
	nv := r.next
	r.next = nextVisit{}

	var seq, number int
	var cameFrom, dirName, sessionID, resumeID, resumeKind string
	var plan sessionPlan
	var started time.Time
	gotSlot := false
	agentLike := st.IsAgentLike()
	if resume == nil {
		seq = len(s.Visits) + 1
		number = s.VisitTotals[name] + 1
		cameFrom = s.CameFrom
		dirName = visitDirName(seq, name)
		if err := os.MkdirAll(filepath.Join(r.dir, store.VisitsDir, dirName), 0o700); err != nil {
			r.park("can't create visit dir: " + err.Error())
			return true
		}
		plan = r.sessions(s, name, st, nv)
		sessionID, resumeID, resumeKind = plan.sessionID, plan.resumeID, plan.kind
		head := ""
		if s.Workspace != nil {
			head = gitws.Tip(context.Background(), s.Workspace.Path, "HEAD")
		}
		if agentLike {
			gotSlot = r.e.tryAcquireSlot()
		}
		if err := r.emit(store.EvVisitStarted, store.VisitStarted{
			Seq: seq, Step: name, Type: typ, VisitNumber: number, CameFrom: cameFrom,
			SessionID: sessionID, ResumeID: resumeID, Queued: agentLike && !gotSlot, Dir: dirName,
			Thread: plan.thread, HeadSHA: head,
		}); err != nil {
			if gotSlot {
				r.e.releaseSlot()
			}
			return false
		}
		started = time.Now()
		if typ == pipeline.TypeRun || agentLike {
			r.setStatus(store.StatusRunning, "")
		}
	} else {
		seq, number, cameFrom, dirName, started = resume.Seq, resume.VisitNumber, resume.CameFrom, resume.Dir, resume.Started
	}
	s = r.snap()

	prevVisit := lastFinishedBefore(s, seq)
	scope := r.scope(s, prevVisit, cameFrom, number, name, seq)
	env, err := stepEnv(st, scope)
	if err != nil {
		env = nil
	}
	fullEnv := proc.MergeEnv(r.e.o.BaseEnv, scope.Env(), env)
	if g := r.e.o.GlobalPipelines; g != "" {
		// Global pipelines reach shared helpers as "$SHIP_HOME/bin/…".
		fullEnv = proc.MergeEnv(fullEnv, []string{brand.HomeEnv + "=" + filepath.Dir(g)})
	}
	forceCLI := ""
	if s.FakeAgents != "" {
		fullEnv = proc.MergeEnv(fullEnv, []string{fake.ScriptEnv + "=" + s.FakeAgents})
		forceCLI = "fake"
	}
	var prev *agent.PrevInfo
	if prevVisit != nil {
		hp := filepath.Join(r.dir, store.VisitsDir, prevVisit.Dir, "handover.md")
		text, _ := os.ReadFile(hp)
		prev = &agent.PrevInfo{Step: prevVisit.Step, Outcome: prevVisit.Outcome, HandoverPath: hp, Handover: agent.TruncateHandover(string(text), hp)}
	}
	worktree := ""
	if s.Workspace != nil {
		worktree = s.Workspace.Path
	}
	v := &steps.Visit{
		RunID: r.id, RunDir: r.dir, Worktree: worktree, Repo: s.Repo, PipelineName: s.Pipeline,
		Pipeline: r.pipe, StepName: name, Step: st, Seq: seq, Number: number,
		Dir: filepath.Join(r.dir, store.VisitsDir, dirName), CameFrom: cameFrom, Scope: scope, Env: fullEnv,
		Timeout: r.pipe.Timeout(st), OutputTail: r.pipe.OutputTail(), Prev: prev, Snapshot: s,
		BriefPath: filepath.Join(r.dir, store.BriefFile), Acceptance: r.brief.AcceptanceMarkdown(),
		RunNotes:  r.runNotes(s),
		SessionID: sessionID, ResumeID: resumeID, ResumeKind: resumeKind, Note: nv.note,
		Thread: plan.thread, Since: r.sinceLastTurn(s, plan.last, worktree),
		ForceCLI: forceCLI, Resumed: resume != nil, StartedAt: started,
	}
	root := worktree
	if root == "" {
		root = s.Repo
	}
	if folder := runFolders(s, root)[s.Pipeline]; folder != "" && st.IsAgentLike() {
		// The folder's skills as a plugin, built in the run's dir (never
		// in the checkout) from the skills as the worktree has them now.
		dir := filepath.Join(r.dir, pluginDir)
		skipped, perr := pipeline.BuildPlugin(dir, folder, s.Pipeline, r.pipe.Description)
		if len(skipped) > 0 {
			r.e.o.Log.Warn("left unreadable entries out of the pipeline's plugin", "run", r.id, "skipped", skipped)
		}
		if perr != nil {
			r.e.o.Log.Warn("building the pipeline's plugin", "run", r.id, "err", perr)
		} else {
			v.PluginDirs = []string{dir}
		}
	}
	if err != nil {
		// Env rendering failed: report it as the visit's error.
		if gotSlot {
			r.e.releaseSlot()
		}
		return r.finishVisit(v, typ, visitResult{res: renderErr(err)}, nil, started)
	}
	rt := &visitRT{r: r, seq: seq}
	v.RT = rt

	vctx, vcancel := context.WithCancel(context.Background())
	defer vcancel()
	resCh := make(chan visitResult, 1)
	go func() {
		held := false
		var idleOnce sync.Once
		rt.idle = func() {
			idleOnce.Do(func() {
				if held {
					r.e.releaseSlot()
				}
				r.executing.Store(false)
			})
		}
		defer rt.Idle()
		if agentLike && resume == nil {
			if !gotSlot {
				if err := r.e.acquireSlot(vctx); err != nil {
					resCh <- visitResult{res: steps.Result{Outcome: steps.OutcomeCancelled, Summary: "cancelled while queued"}}
					return
				}
				r.emit(store.EvVisitDequeued, store.SeqOnly{Seq: seq})
			}
			held = true
		}
		if typ == pipeline.TypeRun || (agentLike && resume == nil) {
			r.executing.Store(true)
		}
		res, err := r.executor(typ).Execute(vctx, v)
		resCh <- visitResult{res, err}
	}()

	var abort *abortReq
	for {
		select {
		case vr := <-resCh:
			r.dropVisitCmds()
			return r.finishVisit(v, typ, vr, abort, started)
		case c := <-r.cmds:
			if a := r.duringVisit(c, v); a != nil && abort == nil {
				abort = a
				vcancel()
			}
		case <-r.ctx.Done():
			vcancel()
			<-resCh
			r.dropVisitCmds()
			r.interrupted(v, typ)
			return false
		}
	}
}

// dropVisitCmds rejects commands queued for a visit that has ended, so they
// can't reach the next visit.
func (r *runner) dropVisitCmds() {
	for {
		select {
		case c := <-r.visitCmds:
			c.Respond(conflict("the step ended before the command was taken"))
		case c := <-r.sliceCmds:
			reply(c, conflict("the run stopped running its slices before the command was taken"))
		default:
			return
		}
	}
}

func renderErr(err error) steps.Result {
	var unset *tmpl.UnsetVarError
	if errors.As(err, &unset) {
		return steps.ErrorResult("unset_var:"+unset.Name, err.Error())
	}
	return steps.ErrorResult("template", err.Error())
}

func lastFinishedBefore(s *store.RunSnapshot, seq int) *store.VisitSummary {
	for i := len(s.Visits) - 1; i >= 0; i-- {
		v := &s.Visits[i]
		if v.Seq < seq && v.Finished != nil {
			return v
		}
	}
	return nil
}

func (r *runner) scope(s *store.RunSnapshot, prev *store.VisitSummary, cameFrom string, number int, step string, seq int) *tmpl.Scope {
	in := scopeInput{pipe: r.pipe, snap: s, runDir: r.dir, brief: r.brief, prev: prev, cameFrom: cameFrom, visitNumber: number, step: step, seq: seq}
	if s.Parent != nil {
		if ps, err := r.e.Snapshot(s.Parent.ID); err == nil {
			in.parent = ps
			in.parentDir = r.e.o.Store.RunDir(ps.ID)
			if b, err := os.ReadFile(filepath.Join(in.parentDir, store.BriefFile)); err == nil {
				in.parentBrief, _ = brief.Parse(b)
			}
		}
	}
	return buildScope(in)
}

// duringVisit handles a command while a visit executes. It returns a
// non-nil abort when the visit must end (cancel, goto, retry).
func (r *runner) duringVisit(c Command, v *steps.Visit) *abortReq {
	switch c.Name {
	case CmdPRTrigger:
		// A pr step always returns to awaiting between polls, so a trigger
		// that arrives mid-poll is queued for it.
		if v.Step.Type() != pipeline.TypePR {
			reply(c, conflict("the run isn't watching a PR right now"))
			return nil
		}
		sc := steps.Command{Name: steps.PRTrigger, Reply: make(chan error, 1)}
		select {
		case r.visitCmds <- sc:
		default:
			reply(c, conflict("another command is being processed"))
			return nil
		}
		r.emitUser(c)
		go func() {
			select {
			case err := <-sc.Reply:
				var inv *steps.InvalidError
				if errors.As(err, &inv) {
					err = invalid("%s", inv.Msg)
				}
				reply(c, err)
			case <-time.After(time.Minute):
				reply(c, conflict("the trigger wasn't taken"))
			}
		}()
	case CmdAnswer, CmdSplitReview:
		// The status turns to asking just before the executor starts
		// awaiting; a pending ask for this visit is enough to queue it.
		if pa := r.snap().PendingAsk; !r.awaiting.Load() && (pa == nil || pa.Seq != v.Seq) {
			reply(c, conflict("the run isn't waiting for an answer"))
			return nil
		}
		switch c.For {
		case "", store.NoteForStep, store.NoteForRun:
		default:
			reply(c, invalid("a note is for %q or %q, not %q", store.NoteForStep, store.NoteForRun, c.For))
			return nil
		}
		sc := steps.Command{Name: "answer", Choice: c.Choice, Note: c.Note, For: c.For, Action: c.Action, Reply: make(chan error, 1)}
		if c.Name == CmdSplitReview {
			sc.Name = "split_review"
		}
		select {
		case r.visitCmds <- sc:
		default:
			reply(c, conflict("another answer is being processed"))
			return nil
		}
		r.emitUser(c)
		go func() {
			select {
			case err := <-sc.Reply:
				var inv *steps.InvalidError
				if errors.As(err, &inv) {
					err = invalid("%s", inv.Msg)
				}
				reply(c, err)
			case <-time.After(time.Minute):
				reply(c, conflict("the answer wasn't taken"))
			}
		}()
	case CmdSetVar:
		reply(c, r.setVar(c))
	case CmdCancel:
		r.emitUser(c)
		return &abortReq{cancel: true, cmd: c}
	case CmdGoto:
		if err := r.checkTarget(c.Step); err != nil {
			reply(c, err)
			return nil
		}
		r.emitUser(c)
		return &abortReq{to: c.Step, cmd: c}
	case CmdRaiseBudget:
		reply(c, r.raiseBudget(c))
	case CmdUpgrade:
		// The visit keeps its own step; it ends as a goto would.
		if err := r.upgrade(c); err != nil {
			reply(c, err)
			return nil
		}
		return &abortReq{to: c.Step, cmd: c}
	case CmdRetry:
		s := r.snap()
		if s.Status != store.StatusAsking || s.PendingAsk == nil || s.PendingAsk.Kind != store.AskKindAsk || v.CameFrom == "" {
			reply(c, conflict("retry is available when the run needs attention or a check-in is pending"))
			return nil
		}
		r.emitUser(c)
		return &abortReq{to: v.CameFrom, cmd: c}
	case CmdStartSlice:
		if v.Step.Type() != pipeline.TypeFanout {
			reply(c, conflict("the run isn't running its slices right now"))
			return nil
		}
		if len(r.sliceCmds) > 0 {
			reply(c, conflict("another slice is being started"))
			return nil
		}
		r.emitUser(c)
		r.sliceCmds <- c // only this goroutine sends, so there's room
	case CmdParentHalted:
		r.halted = true
		reply(c, nil)
	case CmdPause:
		// A fanout supervisor reflects this in its status when woken.
		r.requestPause(c)
		reply(c, nil)
	case CmdResume:
		r.requestResume(c)
		reply(c, nil)
	default:
		reply(c, conflict("%s isn't possible while a step is running", c.Name))
	}
	return nil
}

// raiseBudget adds to the run's budget. A run without a limit of that kind
// has nothing to raise.
func (r *runner) raiseBudget(c Command) error {
	if c.USD < 0 || c.Tokens < 0 || (c.USD == 0 && c.Tokens == 0) {
		return invalid("say how much to add: dollars, tokens or both")
	}
	if c.USD > 0 && r.pipe.MaxBudget() == 0 {
		return invalid("the run has no dollar budget (limits.max_budget_usd) to raise")
	}
	if c.Tokens > 0 && r.pipe.MaxTokens() == 0 {
		return invalid("the run has no token budget (limits.max_tokens) to raise")
	}
	r.emitUser(c)
	return r.emit(store.EvBudgetRaised, store.BudgetRaised{USD: c.USD, Tokens: c.Tokens})
}

// upgrade swaps in the pipeline Engine.Upgrade staged next to the run's
// snapshot and versions the run again. c.Step must be a step of it.
func (r *runner) upgrade(c Command) error {
	pd := filepath.Join(r.dir, store.PipelineDir)
	stage, old := pd+".next", pd+".old"
	s := r.snap()
	f, findings := pipeline.NewLoader(stage).Load(s.Pipeline)
	if f == nil {
		msg := "the new pipeline wasn't staged"
		if len(findings) > 0 {
			msg = findings[0].String()
		}
		return invalid("%s", msg)
	}
	if _, ok := f.Pipeline.Steps[c.Step]; !ok {
		return invalid("unknown step %q (steps: %s)", c.Step, strings.Join(f.Pipeline.SortedSteps(), ", "))
	}
	_ = os.RemoveAll(old)
	if err := os.Rename(pd, old); err != nil {
		return err
	}
	if err := os.Rename(stage, pd); err != nil {
		_ = os.Rename(old, pd)
		return err
	}
	_ = os.RemoveAll(old)
	r.pipe = f.Pipeline
	r.emitUser(c)
	r.emit(store.EvUpgraded, store.Upgraded{FromVersion: s.PipelineVersion, FromHash: s.PipelineHash, Step: c.Step, Warnings: c.Warnings, PipelineFolders: c.Folders})
	r.versionPipeline()
	return nil
}

func (r *runner) checkTarget(step string) error {
	if step == pipeline.TargetDone || step == pipeline.TargetStop {
		return nil
	}
	if _, ok := r.pipe.Steps[step]; !ok {
		return invalid("unknown step %q (steps: %s)", step, strings.Join(r.pipe.SortedSteps(), ", "))
	}
	return nil
}

func (r *runner) setVar(c Command) error {
	if _, ok := r.pipe.Variables[c.Var]; !ok {
		return invalid("unknown variable %q", c.Var)
	}
	if err := CheckFormat(r.pipe, c.Var, c.Value); err != nil {
		return err
	}
	r.emitUser(c)
	_, err := r.log.Emit(store.EvVarSet, store.ActorUser, store.VarSet{Name: c.Var, Value: c.Value})
	return err
}

// finishVisit records the result and routes. It returns false when the
// goroutine should exit.
func (r *runner) finishVisit(v *steps.Visit, typ string, vr visitResult, abort *abortReq, started time.Time) bool {
	res := vr.res
	if vr.err != nil {
		res = steps.ErrorResult("engine", vr.err.Error())
		abort = nil
	}
	if abort != nil {
		res = steps.Result{Outcome: steps.OutcomeCancelled, Summary: "Ended by " + abort.cmd.Source + " (" + abort.cmd.Name + ")", Cost: res.Cost, Tokens: res.Tokens, TokenUsage: res.TokenUsage, SessionID: res.SessionID}
	} else if res.Outcome == steps.OutcomeCancelled {
		// Cancelled without an abort (shouldn't happen): treat as an error.
		res = steps.ErrorResult("cancelled", "the visit was cancelled")
	}
	// Saved vars must be declared and match their format.
	if res.Outcome != steps.OutcomeError && abort == nil {
		for _, k := range sortedKeys(res.Vars) {
			if err := CheckFormat(r.pipe, k, res.Vars[k]); err != nil {
				e := steps.ErrorResult("format:"+k, err.Error())
				e.Cost, e.Tokens, e.TokenUsage, e.SessionID, e.Output = res.Cost, res.Tokens, res.TokenUsage, res.SessionID, res.Output
				res = e
				break
			}
		}
	}
	finished := time.Now()
	dur := finished.Sub(started).Milliseconds()
	_ = writeResult(v.File("result.json"), resultJSON{
		Seq: v.Seq, Step: v.StepName, Type: typ, Outcome: res.Outcome, Summary: res.Summary, Vars: res.Vars,
		Error: res.Error, ExitCode: res.ExitCode, CostUSD: res.Cost, Tokens: res.Tokens, Usage: res.Usage, SessionID: res.SessionID,
		DurationMS: dur, PermissionDenials: res.PermissionDenials, Polls: res.Polls, Extra: res.Extra,
	})
	_ = writeHandover(v.File("handover.md"), r.id, v.StepName, v.Number, res, finished, r.pipe.OutputTail())
	if err := r.emit(store.EvVisitFinished, store.VisitFinished{
		Seq: v.Seq, Outcome: res.Outcome, Summary: res.Summary, Vars: res.Vars, Error: res.Error,
		CostUSD: res.Cost, Tokens: res.Tokens, Usage: res.TokenUsage.Ptr(), DurationMS: dur, SessionID: res.SessionID, PermissionDenials: len(res.PermissionDenials),
	}); err != nil {
		return false
	}
	if abort == nil {
		for _, k := range sortedKeys(res.Vars) {
			r.emit(store.EvVarSet, store.VarSet{Name: k, Value: res.Vars[k], ByStep: v.StepName})
		}
	}

	if abort != nil {
		if abort.cancel {
			r.cancelRun()
			reply(abort.cmd, nil)
			return false
		}
		r.halted = false
		r.transition(v.StepName, abort.to, "", store.ReasonManual, true)
		r.setStatus(store.StatusRunning, "")
		reply(abort.cmd, nil)
		return true
	}
	r.route(v, res)
	return true
}

// route resolves the outcome and transitions.
func (r *runner) route(v *steps.Visit, res steps.Result) {
	st := v.Step
	switch res.Internal {
	case "stop":
		r.transition(v.StepName, pipeline.TargetStop, res.Outcome, store.ReasonHuman, false)
		return
	case "resplit":
		r.next = nextVisit{resumeKind: "resplit", resumeID: res.SessionID, note: res.Summary}
		r.transition(v.StepName, v.StepName, res.Outcome, store.ReasonHuman, true)
		return
	}
	if res.Outcome == steps.OutcomeError && res.Error != nil && res.Error.Reason == "run_budget" {
		// Retrying can't help until someone raises the budget, so hold the
		// run for them rather than routing to an error handler.
		r.park(fmt.Sprintf("budget reached at %s (%s)", v.StepName, res.Error.Message))
		return
	}
	if res.Outcome == steps.OutcomeError {
		t := r.pipe.OnError(st)
		reason := store.ReasonError
		if res.Error != nil && res.Error.Reason == "budget" {
			reason = store.ReasonBudget
		}
		if t == pipeline.TargetCameFrom {
			t = v.CameFrom
		}
		if t == "" {
			msg := "error"
			if res.Error != nil {
				msg = res.Error.Reason + ": " + res.Error.Message
			}
			r.park(fmt.Sprintf("error at %s: %s", v.StepName, msg))
			return
		}
		r.transition(v.StepName, t, res.Outcome, reason, false)
		return
	}
	t, ok := st.Target(res.Outcome)
	if !ok || t == "" {
		r.park(fmt.Sprintf("step %q produced outcome %q, which isn't mapped", v.StepName, res.Outcome))
		return
	}
	if t == pipeline.TargetCameFrom {
		t = v.CameFrom
		if t == "" {
			r.park(fmt.Sprintf("step %q chose $came_from, but nothing came before it", v.StepName))
			return
		}
	}
	reason := store.ReasonNormal
	if res.HumanReset {
		reason = store.ReasonHuman
	}
	r.transition(v.StepName, t, res.Outcome, reason, res.HumanReset)
}

func (r *runner) transition(from, to, outcome, reason string, reset bool) {
	r.emit(store.EvTransition, store.Transition{From: from, To: to, Outcome: outcome, Reason: reason, Reset: reset})
	r.checkBaseDrift()
}

// checkBaseDrift flags a child whose base branch moved (, advisory).
func (r *runner) checkBaseDrift() {
	s := r.snap()
	if s.Parent == nil || s.BaseMoved || s.Workspace == nil || s.Workspace.BaseSHA == "" || s.Provider == parentProvider {
		return
	}
	ref := s.Workspace.Base
	if ref == "" {
		return
	}
	tip := gitws.Tip(context.Background(), s.Repo, ref)
	if tip != "" && tip != s.Workspace.BaseSHA {
		r.emit(store.EvBaseMoved, store.BaseMoved{OldSHA: s.Workspace.BaseSHA, NewSHA: tip})
	}
}

// park sets needs_attention and notifies.
func (r *runner) park(reason string) {
	r.setStatus(store.StatusNeedsAttention, reason)
	r.e.o.Notifier.Notify(brand.Name+": "+r.snap().Title, "Needs attention: "+reason)
}

// interrupted records a visit cut short by shutdown. Asks, waits, fanouts
// and split reviews resume on restart; agent and run visits are parked.
func (r *runner) interrupted(v *steps.Visit, typ string) {
	s := r.snap()
	if typ == pipeline.TypeAsk || typ == pipeline.TypeWait || typ == pipeline.TypeFanout || typ == pipeline.TypePR {
		return
	}
	if s.PendingAsk != nil && s.PendingAsk.Seq == v.Seq {
		return
	}
	if vs := s.Visit(v.Seq); vs == nil || !vs.Running() {
		return
	}
	r.emit(store.EvVisitInterrupted, store.SeqOnly{Seq: v.Seq})
	if s.Status == store.StatusWaiting && strings.HasPrefix(s.StatusReason, steps.LimitWaitPrefix) {
		r.setStatus(store.StatusNeedsAttention, limitInterrupted)
		return
	}
	r.setStatus(store.StatusNeedsAttention, "interrupted: "+brand.Name+" stopped while this step was running")
}

// limitInterrupted marks a run stopped while waiting out a usage limit; it
// carries on by itself when ship starts again.
const limitInterrupted = "interrupted while waiting out a usage limit; carries on when " + brand.Name + " restarts"

// resumeAfterLimit continues an agent visit that a restart cut off while it
// waited out a usage limit, in the same conversation. It reports whether it
// did.
func (r *runner) resumeAfterLimit(lv *store.VisitSummary) bool {
	if lv == nil || lv.SessionID == "" {
		return false
	}
	r.halted = false
	r.next = nextVisit{resumeKind: "interrupted", resumeID: lv.SessionID}
	r.transition(lv.CameFrom, lv.Step, "", store.ReasonNormal, false)
	r.setStatus(store.StatusRunning, "")
	return true
}

// parked waits for a human command while the run needs attention. It
// returns false when the goroutine should exit.
func (r *runner) parked() bool {
	for {
		select {
		case <-r.ctx.Done():
			return false
		case c := <-r.cmds:
			s := r.snap()
			switch c.Name {
			case CmdRaiseBudget:
				if err := r.raiseBudget(c); err != nil {
					reply(c, err)
					continue
				}
				if c.Action != "retry" {
					reply(c, nil)
					continue
				}
				r.halted = false
				target := s.CurrentStep
				if lv := s.LastVisit(); lv != nil && lv.Interrupted {
					target = lv.Step
				}
				r.transition(s.CameFrom, target, "", store.ReasonManual, true)
				r.setStatus(store.StatusRunning, "")
				reply(c, nil)
				return true
			case CmdRetry:
				r.emitUser(c)
				r.halted = false
				if s.Workspace == nil && len(s.Visits) == 0 {
					r.setStatus(store.StatusStarting, "retrying workspace")
					reply(c, nil)
					return true
				}
				target := s.CurrentStep
				if lv := s.LastVisit(); lv != nil && lv.Interrupted {
					target = lv.Step
				}
				r.transition(s.CameFrom, target, "", store.ReasonManual, true)
				r.setStatus(store.StatusRunning, "")
				reply(c, nil)
				return true
			case CmdGoto:
				if err := r.checkTarget(c.Step); err != nil {
					reply(c, err)
					continue
				}
				r.emitUser(c)
				r.halted = false
				r.transition(s.CurrentStep, c.Step, "", store.ReasonManual, true)
				r.setStatus(store.StatusRunning, "")
				reply(c, nil)
				return true
			case CmdUpgrade:
				if err := r.upgrade(c); err != nil {
					reply(c, err)
					continue
				}
				r.halted = false
				r.transition(s.CurrentStep, c.Step, "", store.ReasonManual, true)
				r.setStatus(store.StatusRunning, "")
				reply(c, nil)
				return true
			case CmdResumeSession:
				lv := s.LastVisit()
				if lv == nil || !lv.Interrupted || lv.SessionID == "" {
					reply(c, conflict("there's no interrupted agent session to resume"))
					continue
				}
				r.emitUser(c)
				r.halted = false
				r.next = nextVisit{resumeKind: "interrupted", resumeID: lv.SessionID}
				r.transition(lv.CameFrom, lv.Step, "", store.ReasonManual, true)
				r.setStatus(store.StatusRunning, "")
				reply(c, nil)
				return true
			case CmdReacquire:
				r.emitUser(c)
				err := r.reacquire()
				reply(c, err)
				if err == nil {
					return true
				}
			case CmdSetVar:
				reply(c, r.setVar(c))
			case CmdCancel:
				r.emitUser(c)
				r.cancelRun()
				reply(c, nil)
				return false
			case CmdParentHalted:
				r.halted = true
				reply(c, nil)
			case CmdPause:
				reply(c, conflict("the run is already stopped and waiting for you"))
			case CmdResume:
				// Clears a pause requested earlier; the run stays parked.
				r.requestResume(c)
				reply(c, nil)
			default:
				reply(c, conflict("%s isn't possible while the run needs attention", c.Name))
			}
		}
	}
}

// reacquire gets a fresh worktree on the same branch.
func (r *runner) reacquire() error {
	s := r.snap()
	if s.Provider == parentProvider {
		return conflict("this run reuses its parent's worktree")
	}
	if s.Workspace != nil && r.provider.Exists(*s.Workspace) {
		return conflict("the worktree still exists at %s", s.Workspace.Path)
	}
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Minute)
	defer cancel()
	lease, err := r.provider.Acquire(ctx, workspace.AcquireRequest{Repo: s.Repo, RunID: s.ID, Branch: s.Branch, Base: s.Base, Env: r.e.o.BaseEnv})
	if err != nil {
		return conflict("re-acquire failed: %v", err)
	}
	if lease.Base == "" {
		lease.Base = s.Base
	}
	r.emit(store.EvWorkspaceAcquired, store.WorkspaceAcquired{Lease: lease})
	if len(s.Visits) == 0 {
		r.setStatus(store.StatusRunning, "")
	} else {
		r.setStatus(store.StatusNeedsAttention, "worktree re-acquired: retry the step or go to another")
	}
	return nil
}

// askVars pauses for `ask:` variables the step uses that aren't set yet.
// ok=false means the state changed (alive tells whether to keep looping).
func (r *runner) askVars(st *pipeline.Step) (ok, alive bool) {
	for _, name := range askVarsNeeded(r.pipe, st) {
		s := r.snap()
		if _, set := s.Vars[name]; set {
			continue
		}
		vr := r.pipe.Variables[name]
		if s.PendingAsk == nil || s.PendingAsk.Var != name {
			q := pipeline.Str(vr.Ask)
			if rendered, err := tmpl.Render(q, r.scope(s, nil, s.CameFrom, 0, s.CurrentStep, 0), tmpl.Plain); err == nil {
				q = rendered
			}
			r.emit(store.EvAskPending, store.AskPending{Seq: 0, Kind: store.AskKindVar, Question: q, Input: "required", Var: name})
			r.e.o.Notifier.Notify(brand.Name+": "+s.Title, q)
		}
		r.setStatus(store.StatusAsking, "variable "+name)
		for answered := false; !answered; {
			select {
			case <-r.ctx.Done():
				return false, false
			case c := <-r.cmds:
				switch c.Name {
				case CmdAnswer, CmdSetVar:
					val := c.Note
					if c.Name == CmdSetVar {
						if c.Var != name {
							reply(c, r.setVar(c))
							continue
						}
						val = c.Value
					} else if val == "" {
						val = c.Choice
					}
					if strings.TrimSpace(val) == "" {
						reply(c, invalid("a value for %s is required", name))
						continue
					}
					if err := CheckFormat(r.pipe, name, val); err != nil {
						reply(c, err)
						continue
					}
					r.emitUser(c)
					r.emit(store.EvAskAnswered, store.AskAnswered{Seq: 0, Note: val})
					_, _ = r.log.Emit(store.EvVarSet, store.ActorUser, store.VarSet{Name: name, Value: val})
					reply(c, nil)
					answered = true
				case CmdCancel:
					r.emitUser(c)
					r.emit(store.EvAskAnswered, store.AskAnswered{Seq: 0, Choice: "cancel"})
					r.cancelRun()
					reply(c, nil)
					return false, false
				case CmdGoto:
					if err := r.checkTarget(c.Step); err != nil {
						reply(c, err)
						continue
					}
					r.emitUser(c)
					r.emit(store.EvAskAnswered, store.AskAnswered{Seq: 0, Choice: "skipped"})
					r.transition(s.CurrentStep, c.Step, "", store.ReasonManual, true)
					r.setStatus(store.StatusRunning, "")
					reply(c, nil)
					return false, true
				case CmdParentHalted:
					r.halted = true
					reply(c, nil)
				case CmdPause:
					r.requestPause(c)
					reply(c, nil)
				case CmdResume:
					r.requestResume(c)
					reply(c, nil)
				default:
					reply(c, conflict("the run is waiting for variable %s", name))
				}
			}
		}
		r.setStatus(store.StatusRunning, "")
	}
	return true, true
}

// askVarsNeeded lists `ask:` variables referenced by a step's templates.
func askVarsNeeded(p *pipeline.Pipeline, st *pipeline.Step) []string {
	var srcs []string
	for _, ptr := range []*string{st.Agent, st.Prompt, st.Split, st.Run, st.Wait, st.Ask} {
		if ptr != nil {
			srcs = append(srcs, *ptr)
		}
	}
	for _, v := range st.Env {
		srcs = append(srcs, v)
	}
	seen := map[string]bool{}
	var out []string
	for _, src := range srcs {
		refs, _ := tmpl.Refs(src)
		for _, ref := range refs {
			name, ok := strings.CutPrefix(ref.Path, "vars.")
			if !ok || seen[name] {
				continue
			}
			if v := p.Variables[name]; v != nil && v.Source() == pipeline.SourceAsk {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// cancelRun cancels the run and its children.
func (r *runner) cancelRun() {
	s := r.snap()
	for _, c := range s.Children {
		if !c.Status.Terminal() {
			child := c.ID
			go r.e.Do(child, Command{Name: CmdCancel, Source: "engine"})
		}
	}
	r.finish(store.StatusCancelled, "cancelled")
}

// finish ends the run and applies the release policy.
func (r *runner) finish(status store.Status, reason string) {
	// Release first, so anyone seeing the terminal status sees the cleanup too.
	if status == store.StatusDone && r.cfg.Workspace.ReleaseOnDone {
		r.release(false)
	}
	// Record the stats before the run is seen to finish, so anyone who
	// waits for the terminal status can read them.
	final := *r.snap()
	now := time.Now().UTC()
	final.Status, final.StatusReason, final.FinishedAt, final.PendingAsk = status, reason, &now, nil
	r.e.RecordRunStats(&final, r.cfg)
	r.emit(store.EvRunFinished, store.RunFinished{Status: status, Reason: reason})
}

// recover re-enters a run after a restart. It returns false when
// the goroutine should exit.
func (r *runner) recover() bool {
	s := r.snap()
	if s.Workspace != nil && !r.provider.Exists(*s.Workspace) {
		if !s.WorkspaceMissing {
			r.emit(store.EvWorkspaceMissing, store.WorkspaceMissing{Path: s.Workspace.Path})
		}
		if lv := s.LastVisit(); lv != nil && lv.Running() {
			r.emit(store.EvVisitInterrupted, store.SeqOnly{Seq: lv.Seq})
		}
		r.park("worktree_missing: " + s.Workspace.Path + " is gone; re-acquire it or stop the run")
		return true
	}
	lv := s.LastVisit()
	running := lv != nil && lv.Running()
	// Stopped while waiting out a usage limit: carry on (if the limit
	// hasn't reset yet, the step waits again).
	if s.Status == store.StatusNeedsAttention && s.StatusReason == limitInterrupted && lv != nil && lv.Interrupted {
		if r.resumeAfterLimit(lv) {
			return true
		}
	}
	if running && s.Status == store.StatusWaiting && strings.HasPrefix(s.StatusReason, steps.LimitWaitPrefix) {
		r.emit(store.EvVisitInterrupted, store.SeqOnly{Seq: lv.Seq})
		if r.resumeAfterLimit(r.snap().LastVisit()) {
			return true
		}
	}
	// An agent can't be re-entered mid-invocation; the only agent-like
	// visit that resumes is a split waiting for its review. Anything else
	// (such as an agent waiting out a usage limit) was interrupted.
	if running {
		if st := r.pipe.Steps[lv.Step]; st != nil && st.IsAgentLike() && (s.PendingAsk == nil || s.PendingAsk.Seq != lv.Seq) {
			r.emit(store.EvVisitInterrupted, store.SeqOnly{Seq: lv.Seq})
			r.park("interrupted: " + brand.Name + " stopped while this step was running")
			return true
		}
	}
	switch s.Status {
	case store.StatusAsking, store.StatusWaiting, store.StatusFannedOut, store.StatusPaused:
		if running {
			if st := r.pipe.Steps[lv.Step]; st != nil {
				return r.visit(lv.Step, st, lv)
			}
		}
	case store.StatusRunning:
		if running {
			r.emit(store.EvVisitInterrupted, store.SeqOnly{Seq: lv.Seq})
			r.park("interrupted: " + brand.Name + " stopped while this step was running")
			return true
		}
		if lv != nil && !r.transitionedAfterLastVisit() {
			r.park("interrupted between steps: " + brand.Name + " stopped before routing " + lv.Step)
		}
	}
	return true
}

func (r *runner) transitionedAfterLastVisit() bool {
	evs, _, err := store.ReadEvents(r.dir)
	if err != nil {
		return true
	}
	lastFinished, lastTransition := 0, 0
	for _, ev := range evs {
		switch ev.Type {
		case store.EvVisitFinished:
			lastFinished = ev.Seq
		case store.EvTransition:
			lastTransition = ev.Seq
		}
	}
	return lastTransition > lastFinished
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- runtime for executors ---------------------------------------------------

type visitRT struct {
	r    *runner
	seq  int
	idle func()
}

func (v *visitRT) Idle() {
	if v.idle != nil {
		v.idle()
	}
}

func (v *visitRT) Emit(typ string, data any) error { return v.r.emit(typ, data) }
func (v *visitRT) Executing(on bool)               { v.r.executing.Store(on) }
func (v *visitRT) SetStatus(st store.Status, reason string) error {
	return v.r.setStatus(st, reason)
}
func (v *visitRT) Output(stream string, chunk []byte) {
	if p := v.r.e.o.Publisher; p != nil {
		p.Output(v.r.id, v.seq, stream, chunk)
	}
}
func (v *visitRT) AgentEvent(ev agent.UIEvent) {
	if rep, ok := ev.Data.(agent.UsageReport); ok && ev.Kind == "usage" {
		v.r.e.observeUsage(rep)
		return
	}
	if p := v.r.e.o.Publisher; p != nil {
		p.Agent(v.r.id, v.seq, ev)
	}
}
func (v *visitRT) Await(ctx context.Context) (steps.Command, error) {
	v.r.awaiting.Store(true)
	defer v.r.awaiting.Store(false)
	select {
	case c := <-v.r.visitCmds:
		return c, nil
	case <-ctx.Done():
		return steps.Command{}, ctx.Err()
	}
}
func (v *visitRT) Agents() *agent.Registry         { return v.r.e.o.Agents }
func (v *visitRT) AgentBase() pipeline.AgentConfig { return v.r.cfg.Agent }
func (v *visitRT) Notify(title, message string) {
	v.r.e.o.Notifier.Notify(brand.Name+": "+title, message)
}
func (v *visitRT) Snapshot() *store.RunSnapshot { return v.r.snap() }

// --- pause and resume ----------------------------------------------------------

// requestPause records a pause. It takes effect at the next step boundary;
// with All, it's passed on to running child runs.
func (r *runner) requestPause(c Command) {
	r.emitUser(c)
	if !r.paused.Load() {
		r.paused.Store(true)
		r.emit(store.EvPauseRequested, store.PauseChange{All: c.All, Source: c.Source})
	}
	if c.All {
		r.forChildren(Command{Name: CmdPause, All: true, Source: "engine"})
	}
	r.poke()
}

// requestResume clears a pause; with All, child runs resume too.
func (r *runner) requestResume(c Command) {
	r.emitUser(c)
	if r.paused.Load() || r.snap().PauseRequested {
		r.paused.Store(false)
		r.emit(store.EvResumed, store.PauseChange{All: c.All, Source: c.Source})
	}
	if c.All {
		r.forChildren(Command{Name: CmdResume, All: true, Source: "engine"})
	}
	r.poke()
}

func (r *runner) forChildren(c Command) {
	for _, ch := range r.snap().Children {
		if !ch.Status.Terminal() {
			id := ch.ID
			go r.e.Do(id, c)
		}
	}
}

func (r *runner) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// holdPaused waits at a step boundary until the run is resumed. It returns
// false when the goroutine should exit.
func (r *runner) holdPaused() bool {
	r.setStatus(store.StatusPaused, "paused")
	for {
		select {
		case <-r.ctx.Done():
			return false
		case c := <-r.cmds:
			switch c.Name {
			case CmdResume:
				r.requestResume(c)
				r.setStatus(store.StatusRunning, "")
				reply(c, nil)
				return true
			case CmdPause:
				reply(c, nil)
			case CmdGoto:
				if err := r.checkTarget(c.Step); err != nil {
					reply(c, err)
					continue
				}
				// Going to a step resumes the run there.
				r.requestResume(Command{Name: CmdResume, Source: c.Source})
				r.emitUser(c)
				s := r.snap()
				r.transition(s.CurrentStep, c.Step, "", store.ReasonManual, true)
				r.setStatus(store.StatusRunning, "")
				reply(c, nil)
				return true
			case CmdUpgrade:
				// Upgrading resumes the run at the chosen step, like goto.
				if err := r.upgrade(c); err != nil {
					reply(c, err)
					continue
				}
				r.requestResume(Command{Name: CmdResume, Source: c.Source})
				s := r.snap()
				r.transition(s.CurrentStep, c.Step, "", store.ReasonManual, true)
				r.setStatus(store.StatusRunning, "")
				reply(c, nil)
				return true
			case CmdSetVar:
				reply(c, r.setVar(c))
			case CmdRaiseBudget:
				reply(c, r.raiseBudget(c))
			case CmdCancel:
				r.emitUser(c)
				r.cancelRun()
				reply(c, nil)
				return false
			case CmdParentHalted:
				r.halted = true
				reply(c, nil)
			default:
				reply(c, conflict("the run is paused; resume it first"))
			}
		}
	}
}
