// Package engine runs pipelines as persistent state machines: one
// goroutine per active run, every state change an event.
package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/brief"
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/notify"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
	"github.com/thehenrymcintosh/ship/internal/tmpl"
	"github.com/thehenrymcintosh/ship/internal/workspace"
	gitws "github.com/thehenrymcintosh/ship/internal/workspace/git"
	"github.com/thehenrymcintosh/ship/internal/workspace/none"
	"github.com/thehenrymcintosh/ship/internal/workspace/treehouse"
)

// Publisher receives live updates (the daemon's SSE hub, the foreground
// printer). Implementations must not block.
type Publisher interface {
	RunEvent(snap *store.RunSnapshot, e store.Event)
	Output(runID string, seq int, stream string, chunk []byte)
	Agent(runID string, seq int, ev agent.UIEvent)
}

// Options configure an Engine.
type Options struct {
	Store      *store.Store
	Agents     *agent.Registry
	LoadConfig func(repo string) (config.Config, error)
	Providers  func(name string, cfg config.Config) (workspace.Provider, error)
	Notifier   notify.Notifier
	BaseEnv    []string
	Publisher  Publisher
	MaxAgents  int
	// Foreground takes a per-run flock so a daemon leaves the run alone.
	Foreground bool
	// GlobalPipelines is the user-wide pipeline dir (~/.ship/pipelines),
	// searched after the repo's own .ship/pipelines.
	GlobalPipelines string
	// ClaudeDir is Claude Code's user dir (default ~/.claude), where
	// user-level skills referenced by pipelines are found.
	ClaudeDir string
	Log       *slog.Logger
}

// Engine runs pipelines.
type Engine struct {
	o       Options
	mu      sync.Mutex
	runners map[string]*runner
	sem     chan struct{}
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup

	lmu       sync.Mutex
	listeners map[int]func(runID string, e store.Event)
	nextL     int
}

// New returns an engine.
func New(o Options) *Engine {
	if o.MaxAgents < 1 {
		o.MaxAgents = 3
	}
	if o.Notifier == nil {
		o.Notifier = notify.Nop{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Providers == nil {
		o.Providers = DefaultProviders
	}
	if o.LoadConfig == nil {
		o.LoadConfig = func(repo string) (config.Config, error) { return config.Defaults(), nil }
	}
	if o.BaseEnv == nil {
		o.BaseEnv = os.Environ()
	}
	if o.ClaudeDir == "" {
		o.ClaudeDir = config.ClaudeDir()
	}
	// Workspace commands see the same environment as steps.
	providers := o.Providers
	o.Providers = func(name string, cfg config.Config) (workspace.Provider, error) {
		p, err := providers(name, cfg)
		if th, ok := p.(*treehouse.Provider); ok && th.Env == nil {
			th.Env = o.BaseEnv
		}
		return p, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{o: o, runners: map[string]*runner{}, sem: make(chan struct{}, o.MaxAgents), ctx: ctx, stop: cancel, listeners: map[int]func(string, store.Event){}}
}

// ResolveProvider turns "auto" (or "") into a concrete provider: treehouse
// when it's on PATH, otherwise git worktrees.
func ResolveProvider(name string) string {
	if name != "" && name != config.ProviderAuto {
		return name
	}
	if _, err := exec.LookPath("treehouse"); err == nil {
		return "treehouse"
	}
	return "git"
}

// DefaultProviders builds the built-in workspace providers.
func DefaultProviders(name string, cfg config.Config) (workspace.Provider, error) {
	switch ResolveProvider(name) {
	case "git":
		return &gitws.Provider{DirTemplate: cfg.Workspace.Git.Dir, Fetch: cfg.Workspace.Fetch, Setup: cfg.Workspace.Git.Setup}, nil
	case "treehouse":
		return &treehouse.Provider{}, nil
	case "none":
		return none.Provider{}, nil
	case parentProvider:
		return reuseProvider{}, nil
	}
	return nil, fmt.Errorf("unknown workspace provider %q", name)
}

// ErrKind classifies engine errors for the API.
type ErrKind int

// Error kinds.
const (
	KindInvalid ErrKind = iota + 1
	KindConflict
	KindNotFound
)

// Error is an engine error with a kind.
type Error struct {
	Kind     ErrKind
	Msg      string
	Findings []pipeline.Finding
}

func (e *Error) Error() string { return e.Msg }

func invalid(format string, a ...any) error {
	return &Error{Kind: KindInvalid, Msg: fmt.Sprintf(format, a...)}
}

func conflict(format string, a ...any) error {
	return &Error{Kind: KindConflict, Msg: fmt.Sprintf(format, a...)}
}

// KindOf returns the kind of err (0 when it isn't an engine error).
func KindOf(err error) ErrKind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	var inv *invalidAnswer
	if errors.As(err, &inv) {
		return KindInvalid
	}
	return 0
}

type invalidAnswer struct{ error }

// --- listeners ---------------------------------------------------------------

func (e *Engine) subscribe(fn func(runID string, ev store.Event)) func() {
	e.lmu.Lock()
	id := e.nextL
	e.nextL++
	e.listeners[id] = fn
	e.lmu.Unlock()
	return func() {
		e.lmu.Lock()
		delete(e.listeners, id)
		e.lmu.Unlock()
	}
}

func (e *Engine) dispatch(runID string, ev store.Event, snap *store.RunSnapshot) {
	if e.o.Publisher != nil {
		e.o.Publisher.RunEvent(snap, ev)
	}
	e.lmu.Lock()
	fns := make([]func(string, store.Event), 0, len(e.listeners))
	for _, fn := range e.listeners {
		fns = append(fns, fn)
	}
	e.lmu.Unlock()
	for _, fn := range fns {
		fn(runID, ev)
	}
}

// --- agent slots -----------------------------------------------------------

func (e *Engine) tryAcquireSlot() bool {
	select {
	case e.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (e *Engine) acquireSlot(ctx context.Context) error {
	select {
	case e.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) releaseSlot() { <-e.sem }

// --- queries ---------------------------------------------------------------

// Snapshot returns the current snapshot of a run (live if active).
func (e *Engine) Snapshot(id string) (*store.RunSnapshot, error) {
	e.mu.Lock()
	r := e.runners[id]
	e.mu.Unlock()
	if r != nil {
		return r.log.Snapshot(), nil
	}
	snap, err := e.o.Store.Load(id)
	if err != nil {
		return nil, &Error{Kind: KindNotFound, Msg: fmt.Sprintf("run %s not found", id)}
	}
	return snap, nil
}

// Active reports whether a run has a live goroutine here.
func (e *Engine) Active(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runners[id] != nil
}

// Done returns a channel closed when the run's goroutine exits (nil if the
// run isn't active).
func (e *Engine) Done(id string) <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r := e.runners[id]; r != nil {
		return r.done
	}
	return nil
}

// Executing counts runs with an agent or run visit in progress.
func (e *Engine) Executing() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, r := range e.runners {
		if r.executing.Load() {
			n++
		}
	}
	return n
}

// Config loads the merged config for a repo.
func (e *Engine) Config(repo string) (config.Config, error) { return e.o.LoadConfig(repo) }

// Agents returns the adapter registry.
func (e *Engine) Agents() *agent.Registry { return e.o.Agents }

// --- commands --------------------------------------------------------------

// Command names.
const (
	CmdAnswer        = "answer"
	CmdSplitReview   = "split_review"
	CmdRetry         = "retry"
	CmdGoto          = "goto"
	CmdSetVar        = "set_var"
	CmdCancel        = "cancel"
	CmdResumeSession = "resume_session"
	CmdReacquire     = "reacquire"
	CmdParentHalted  = "parent_halted"
	CmdPause         = "pause"
	CmdResume        = "resume"
	CmdPRTrigger     = "pr_trigger"
	CmdUpgrade       = "upgrade" // sent by Upgrade, which stages the pipeline
)

// Command is a manual control.
type Command struct {
	Name   string
	Choice string
	Note   string
	Action string
	Step   string
	Var    string
	Value  string
	All    bool // pause/resume: child runs too
	// upgrade: notes on files the run's worktree has an older copy of
	Warnings []string
	Source   string // cli | ui | engine
	reply    chan error
}

// Do sends a command to a run and waits for it to take effect.
func (e *Engine) Do(id string, c Command) error {
	e.mu.Lock()
	r := e.runners[id]
	e.mu.Unlock()
	if r == nil {
		snap, err := e.o.Store.Load(id)
		if err != nil {
			return &Error{Kind: KindNotFound, Msg: fmt.Sprintf("run %s not found", id)}
		}
		if snap.Status.Terminal() {
			return conflict("run %s is %s", id, snap.Status)
		}
		return conflict("run %s isn't active in this process", id)
	}
	if c.Source == "" {
		c.Source = "cli"
	}
	c.reply = make(chan error, 1)
	select {
	case r.cmds <- c:
	case <-r.done:
		return conflict("run %s has finished", id)
	case <-time.After(10 * time.Second):
		return conflict("run %s is busy", id)
	}
	select {
	case err := <-c.reply:
		return err
	case <-r.done:
		// The command ended the run (cancel); that's success.
		select {
		case err := <-c.reply:
			return err
		default:
			return nil
		}
	case <-time.After(2 * time.Minute):
		return conflict("run %s didn't respond", id)
	}
}

// Upgrade moves a run onto its pipeline as it is now (from the repo, or the
// global pipelines) and continues it at step, the current step by default.
// The current visit, if any, ends like a goto. It returns warnings about
// skills, rules and scripts the run's worktree has an older copy of.
func (e *Engine) Upgrade(ctx context.Context, id, step, source string) ([]string, error) {
	snap, err := e.Snapshot(id)
	if err != nil {
		return nil, &Error{Kind: KindNotFound, Msg: fmt.Sprintf("run %s not found", id)}
	}
	if snap.Status.Terminal() {
		return nil, conflict("run %s is %s", id, snap.Status)
	}
	live, err := e.LivePipeline(snap)
	if err != nil {
		return nil, err
	}
	if step == "" {
		step = snap.CurrentStep
	}
	if _, ok := live.Pipeline.Steps[step]; !ok {
		return nil, invalid("step %q isn't in the new %s pipeline; choose one of: %s", step, snap.Pipeline, strings.Join(live.Pipeline.SortedSteps(), ", "))
	}
	closure := live.Closure
	stage := filepath.Join(e.o.Store.RunDir(id), store.PipelineDir+".next")
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return nil, err
	}
	for _, cf := range closure {
		if err := os.WriteFile(filepath.Join(stage, cf.Name+".yml"), cf.Source, 0o600); err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
	}
	var warnings []string
	if snap.Workspace != nil && snap.Provider != "none" && snap.Branch != "" {
		warnings = e.UnsyncedFiles(ctx, snap.Repo, snap.Branch, closure)
	}
	if err := e.Do(id, Command{Name: CmdUpgrade, Step: step, Source: source, Warnings: warnings}); err != nil {
		_ = os.RemoveAll(stage)
		return nil, err
	}
	return warnings, nil
}

// Live is a run's pipeline as it is now, outside the run's snapshot.
type Live struct {
	Pipeline *pipeline.Pipeline
	Closure  []*pipeline.File
	Changed  bool // differs from the run's snapshot
}

// LivePipeline loads and validates a run's pipeline (and the pipelines it
// fans out to) as they are now, the way Start would for a new run.
func (e *Engine) LivePipeline(snap *store.RunSnapshot) (*Live, error) {
	cfg, err := e.o.LoadConfig(snap.Repo)
	if err != nil {
		return nil, invalid("%v", err)
	}
	opts := e.ValidateOptions(cfg)
	opts.IsChild = snap.Parent != nil
	if snap.FakeAgents != "" {
		opts.AgentCheck = nil
	}
	loader := e.Loader(snap.Repo)
	f, findings := loader.Validate(snap.Pipeline, opts)
	if f == nil || pipeline.HasErrors(findings) {
		if len(findings) == 0 {
			findings = []pipeline.Finding{{Severity: pipeline.SevError, Code: "E006", Message: "pipeline " + snap.Pipeline + " not found"}}
		}
		return nil, &Error{Kind: KindInvalid, Msg: "pipeline " + snap.Pipeline + " has errors", Findings: findings}
	}
	closure, err := loader.Closure(snap.Pipeline)
	if err != nil {
		return nil, invalid("%v", err)
	}
	l := &Live{Pipeline: f.Pipeline, Closure: closure}
	dir := filepath.Join(e.o.Store.RunDir(snap.ID), store.PipelineDir)
	entries, _ := os.ReadDir(dir)
	l.Changed = len(entries) != len(closure)
	for _, cf := range closure {
		old, err := os.ReadFile(filepath.Join(dir, cf.Name+".yml"))
		if err != nil || !bytes.Equal(old, cf.Source) {
			l.Changed = true
		}
	}
	return l, nil
}

// --- starting --------------------------------------------------------------

// StartRequest starts a top-level run.
type StartRequest struct {
	Repo       string
	Pipeline   string
	Brief      []byte
	Vars       map[string]string
	FakeAgents string
	child      *childSpec
}

type childSpec struct {
	parent    *store.RunSnapshot
	parentDir string
	step      string
	slice     store.Slice
	count     int
	base      string
	reuse     *workspace.Lease
	id        string
}

// parentProvider marks a child working in its parent's worktree.
const parentProvider = "parent"

var nowFn = time.Now

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewRunID builds `YYYYMMDD-HHMMSS-<slug>-<4 hex>`.
func NewRunID(title string, t time.Time) string {
	return t.Format("20060102-150405") + "-" + brief.Slug(title, 30) + "-" + randHex(2)
}

// ResolvePipeline picks the pipeline name for a start ( step 2).
func ResolvePipeline(l *pipeline.Loader, arg string, b *brief.Brief) (string, error) {
	if arg != "" {
		return arg, nil
	}
	if b != nil && b.Pipeline != "" {
		return b.Pipeline, nil
	}
	names := l.Names()
	switch len(names) {
	case 0:
		return "", invalid("no pipelines in %s (run `%s init`)", strings.Join(l.Dirs, " or "), brand.Name)
	case 1:
		return names[0], nil
	}
	return "", invalid("several pipelines exist; name one: %s", strings.Join(names, ", "))
}

// PipelinesDir is <repo>/.ship/pipelines.
func PipelinesDir(repo string) string { return filepath.Join(repo, brand.Dir, "pipelines") }

// GlobalPipelinesDir is <home>/pipelines.
func GlobalPipelinesDir(home string) string { return filepath.Join(home, "pipelines") }

// Loader returns the pipeline search path for a repo: the repo's own
// pipelines, then the global ones. A repo pipeline shadows a global one of
// the same name.
func (e *Engine) Loader(repo string) *pipeline.Loader {
	var dirs []string
	if repo != "" {
		dirs = append(dirs, PipelinesDir(repo))
	}
	return pipeline.NewLoader(append(dirs, e.o.GlobalPipelines)...)
}

// ValidateOptions returns the validation options for a repo's config.
func (e *Engine) ValidateOptions(cfg config.Config) pipeline.Options {
	return pipeline.Options{AgentCheck: e.o.Agents.Check, AgentBase: cfg.Agent}
}

// Start validates and starts a run.
func (e *Engine) Start(ctx context.Context, req StartRequest) (*store.RunSnapshot, error) {
	if req.child == nil {
		if req.Repo == "" {
			return nil, invalid("repo is required")
		}
		if fi, err := os.Stat(req.Repo); err != nil || !fi.IsDir() {
			return nil, invalid("repo %s doesn't exist", req.Repo)
		}
	}
	cfg, err := e.o.LoadConfig(req.Repo)
	if err != nil {
		return nil, invalid("%v", err)
	}
	b, err := brief.Parse(req.Brief)
	if err != nil {
		return nil, invalid("brief: %v", err)
	}
	// Load and validate the pipeline (children start from the parent's
	// snapshot, then prefer the current file below).
	var loader *pipeline.Loader
	if req.child != nil {
		loader = pipeline.NewLoader(filepath.Join(req.child.parentDir, store.PipelineDir))
	} else {
		loader = e.Loader(req.Repo)
	}
	name := req.Pipeline
	if req.child == nil {
		if name, err = ResolvePipeline(loader, name, b); err != nil {
			return nil, err
		}
	}
	opts := e.ValidateOptions(cfg)
	if req.child != nil {
		opts.IsChild = true
	}
	if req.FakeAgents != "" || (req.child != nil && req.child.parent.FakeAgents != "") {
		// Every agent step runs on the fake adapter, whatever cli it names.
		opts.AgentCheck = nil
	}
	if req.child != nil {
		// A slice uses the child pipeline as it is now, so edits made while
		// the parent runs reach slices that haven't started yet. If the
		// current file doesn't validate, fall back to the parent's copy.
		live := e.Loader(req.Repo)
		if lf, lfs := live.Validate(name, opts); lf != nil && !pipeline.HasErrors(lfs) {
			loader = live
		} else {
			e.o.Log.Warn("slice pipeline doesn't validate as it is now; using the parent's copy", "pipeline", name, "parent", req.child.parent.ID)
		}
	}
	f, findings := loader.Validate(name, opts)
	if f == nil || pipeline.HasErrors(findings) {
		if len(findings) == 0 {
			findings = []pipeline.Finding{{Severity: pipeline.SevError, Code: "E006", Message: "pipeline " + name + " not found"}}
		}
		return nil, &Error{Kind: KindInvalid, Msg: "pipeline " + name + " has errors", Findings: findings}
	}
	p := f.Pipeline

	var parentVars map[string]string
	if req.child != nil {
		parentVars = req.child.parent.Vars
	}
	vars, err := ResolveVars(p, b, req.Vars, parentVars)
	if err != nil {
		return nil, err
	}

	// Run id.
	var id string
	if req.child != nil {
		id = req.child.id
	} else {
		id = NewRunID(b.Title, nowFn())
	}

	// Workspace plan.
	provider := cfg.Workspace.Provider
	if p.Workspace != nil && p.Workspace.Provider != "" {
		provider = p.Workspace.Provider
	}
	// Resolve "auto" now and record the result, so the run keeps the same
	// provider through recovery and cleanup.
	provider = ResolveProvider(provider)
	if req.child != nil && req.child.reuse != nil {
		provider = parentProvider
	}
	if req.child != nil && provider == "none" {
		return nil, invalid("fanout children can't use the none provider; set workspace.reuse: parent")
	}
	if provider != parentProvider {
		prov, err := e.o.Providers(provider, cfg)
		if err != nil {
			return nil, invalid("%v", err)
		}
		if err := prov.Check(ctx, req.Repo); err != nil {
			return nil, invalid("%v", err)
		}
	}
	if provider == "none" {
		if other := e.activeNoneRun(req.Repo); other != "" {
			return nil, conflict("run %s already uses the main checkout of %s (workspace provider none allows one at a time)", other, req.Repo)
		}
	}
	snapForScope := &store.RunSnapshot{ID: id, Pipeline: name, Repo: req.Repo, Vars: vars}
	var parentSnap *store.RunSnapshot
	parentDir := ""
	var parentBrief *brief.Brief
	if req.child != nil {
		parentSnap, parentDir = req.child.parent, req.child.parentDir
		if pb, err := os.ReadFile(filepath.Join(parentDir, store.BriefFile)); err == nil {
			parentBrief, _ = brief.Parse(pb)
		}
		snapForScope.Slice = &store.SliceRef{Key: req.child.slice.Key, Number: req.child.slice.Number, Count: req.child.count, Title: req.child.slice.Title}
	}
	scope := buildScope(scopeInput{pipe: p, snap: snapForScope, runDir: e.o.Store.RunDir(id), brief: b, parent: parentSnap, parentDir: parentDir, parentBrief: parentBrief})

	branch := brand.BranchPrefix + id
	if p.Workspace != nil && p.Workspace.Branch != "" {
		if branch, err = tmpl.Render(p.Workspace.Branch, scope, tmpl.Plain); err != nil {
			return nil, invalid("workspace.branch: %v", err)
		}
	}
	base := ""
	switch {
	case req.child != nil && req.child.base != "":
		base = req.child.base
	case p.Workspace != nil && p.Workspace.Base != "":
		if base, err = tmpl.Render(p.Workspace.Base, scope, tmpl.Plain); err != nil {
			return nil, invalid("workspace.base: %v", err)
		}
	case req.child != nil:
		base = req.child.parent.Base
	default:
		base = gitws.DefaultBranch(ctx, req.Repo)
	}
	if req.child != nil && req.child.reuse != nil {
		branch, base = req.child.reuse.Branch, req.child.reuse.Base
	}

	// Create the run dir, brief and pipeline snapshot.
	rl, err := e.o.Store.Create(id)
	if err != nil {
		return nil, conflict("%v", err)
	}
	dir := rl.Dir()
	fail := func(err error) (*store.RunSnapshot, error) {
		rl.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, store.BriefFile), req.Brief, 0o600); err != nil {
		return fail(err)
	}
	closure, err := loader.Closure(name)
	if err != nil {
		return fail(invalid("%v", err))
	}
	for _, cf := range closure {
		if err := os.WriteFile(filepath.Join(dir, store.PipelineDir, cf.Name+".yml"), cf.Source, 0o600); err != nil {
			return fail(err)
		}
	}
	var lock *store.Lock
	if e.o.Foreground {
		if lock, err = store.TryLock(filepath.Join(dir, store.LockFile)); err != nil {
			return fail(err)
		}
	}

	created := store.RunCreated{
		ID: id, Pipeline: name, Repo: req.Repo, RepoOrigin: gitws.Origin(ctx, req.Repo),
		BriefTitle: b.Title, Start: p.Start, Vars: vars, ShipVersion: brand.Version,
		Provider: provider, Branch: branch, Base: base, FakeAgents: req.FakeAgents,
	}
	// Where the pipeline's history lives. The version itself is recorded
	// once the worktree exists, from what the run's agents will actually use.
	created.HistoryDir = HistoryDir(e.Loader(req.Repo), req.Repo, e.Home(), name)
	if provider != "none" && provider != parentProvider {
		created.Warnings = e.UnsyncedFiles(ctx, req.Repo, base, closure)
	}
	if req.child != nil {
		created.Parent = &store.ParentRef{ID: req.child.parent.ID, Step: req.child.step}
		created.Slice = snapForScope.Slice
		if created.FakeAgents == "" {
			created.FakeAgents = req.child.parent.FakeAgents
		}
	}
	r := e.newRunner(id, rl, lock)
	if _, err := rl.Emit(store.EvRunCreated, store.ActorEngine, created); err != nil {
		return fail(err)
	}
	if err := r.load(); err != nil {
		return fail(err)
	}
	e.launch(r)
	return rl.Snapshot(), nil
}

// ResolveVars resolves start-time variables. parentVars is set for
// child runs, whose brief (the slice's) may set vars of its own; those win
// over the parent's. A pipeline written for fanout can also run on its own:
// its from_parent variables then come from --var or the brief, like
// from_brief ones.
func ResolveVars(p *pipeline.Pipeline, b *brief.Brief, given map[string]string, parentVars map[string]string) (map[string]string, error) {
	out := map[string]string{}
	startable := func(v *pipeline.Variable) bool {
		src := v.Source()
		return src == pipeline.SourceFromBrief || src == pipeline.SourceFromParent
	}
	for k := range given {
		v, ok := p.Variables[k]
		if !ok {
			return nil, invalid("unknown variable %q (the pipeline declares: %s)", k, strings.Join(p.SortedVars(), ", "))
		}
		if !startable(v) {
			return nil, invalid("variable %q isn't from_brief or from_parent, so it can't be set at start", k)
		}
	}
	if parentVars != nil {
		for k := range b.Vars {
			if v, ok := p.Variables[k]; !ok || !startable(v) {
				return nil, invalid("the slice sets variable %q, which this pipeline doesn't take from its brief or parent", k)
			}
		}
	}
	var pendingValues []string
	for _, name := range p.SortedVars() {
		v := p.Variables[name]
		switch v.Source() {
		case pipeline.SourceFromBrief:
			val, ok := given[name]
			if !ok {
				val, ok = b.Vars[name]
			}
			if !ok && parentVars != nil {
				// A child's slice brief carries no vars; inherit by name.
				val, ok = parentVars[name]
			}
			if !ok {
				msg := fmt.Sprintf("missing variable %q: pass --var %s=… or add it under vars: in the brief", name, name)
				if v.Prompt != "" {
					msg += " (" + v.Prompt + ")"
				}
				return nil, &Error{Kind: KindInvalid, Msg: msg}
			}
			out[name] = val
		case pipeline.SourceFromParent:
			val, ok := given[name]
			if !ok {
				val, ok = b.Vars[name]
			}
			if !ok && parentVars != nil {
				val, ok = parentVars[*v.FromParent]
				if !ok {
					return nil, invalid("variable %q copies parent variable %q, which isn't set", name, *v.FromParent)
				}
			}
			if !ok {
				return nil, invalid("missing variable %q: pass --var %s=… or add it under vars: in the brief (it comes from the parent run when this pipeline is used by a fanout)", name, name)
			}
			out[name] = val
		case pipeline.SourceValue:
			pendingValues = append(pendingValues, name)
		}
	}
	// value vars may reference brief.* and other value vars: resolve in passes.
	for pass := 0; len(pendingValues) > 0; pass++ {
		if pass > len(p.Variables) {
			return nil, invalid("variables %s reference each other in a cycle", strings.Join(pendingValues, ", "))
		}
		var still []string
		for _, name := range pendingValues {
			scope := tmpl.NewScope(p.SortedVars())
			for k, v := range out {
				scope.Set("vars."+k, v)
			}
			for _, k := range p.SortedVars() {
				if _, ok := out[k]; !ok {
					scope.MarkUnset("vars." + k)
				}
			}
			scope.Set("brief.title", b.Title)
			scope.Set("brief.acceptance", b.AcceptanceMarkdown())
			val, err := tmpl.Render(*p.Variables[name].Value, scope, tmpl.Plain)
			var unset *tmpl.UnsetVarError
			if errors.As(err, &unset) {
				still = append(still, name)
				continue
			}
			if err != nil {
				return nil, invalid("variable %q: %v", name, err)
			}
			out[name] = val
		}
		pendingValues = still
	}
	for name, val := range out {
		if err := CheckFormat(p, name, val); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CheckFormat enforces a variable's format (full match).
func CheckFormat(p *pipeline.Pipeline, name, val string) error {
	v, ok := p.Variables[name]
	if !ok {
		return invalid("unknown variable %q", name)
	}
	if v.Format == "" {
		return nil
	}
	re, err := regexp.Compile("^(?:" + v.Format + ")$")
	if err != nil {
		return invalid("variable %q has an invalid format: %v", name, err)
	}
	if !re.MatchString(val) {
		return invalid("variable %q = %q doesn't match format %s", name, val, v.Format)
	}
	return nil
}

func (e *Engine) activeNoneRun(repo string) string {
	snaps, _ := e.o.Store.List()
	for _, s := range snaps {
		if s.Repo == repo && s.Provider == "none" && !s.Status.Terminal() {
			return s.ID
		}
	}
	return ""
}

// --- lifecycle -------------------------------------------------------------

func (e *Engine) launch(r *runner) {
	e.mu.Lock()
	e.runners[r.id] = r
	e.mu.Unlock()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			e.mu.Lock()
			delete(e.runners, r.id)
			e.mu.Unlock()
		}()
		r.loop()
	}()
}

// Recover resumes every non-terminal run not held by another process.
func (e *Engine) Recover() (int, error) {
	ids, err := e.o.Store.IDs()
	if err != nil {
		return 0, err
	}
	// Parents first, so children's parent snapshots are loaded consistently.
	sort.Strings(ids)
	n := 0
	for _, id := range ids {
		if e.Active(id) {
			continue
		}
		snap, err := e.o.Store.Load(id)
		if err != nil {
			e.o.Log.Warn("recover: unreadable run", "id", id, "err", err)
			continue
		}
		if snap.Status.Terminal() {
			continue
		}
		dir := e.o.Store.RunDir(id)
		if store.IsLocked(filepath.Join(dir, store.LockFile)) {
			continue // a foreground run owns it
		}
		var lock *store.Lock
		if e.o.Foreground {
			if lock, err = store.TryLock(filepath.Join(dir, store.LockFile)); err != nil {
				continue
			}
		}
		rl, err := e.o.Store.Open(id)
		if err != nil {
			e.o.Log.Warn("recover: can't open run", "id", id, "err", err)
			lock.Unlock()
			continue
		}
		r := e.newRunner(id, rl, lock)
		if err := r.load(); err != nil {
			e.o.Log.Warn("recover: can't load run", "id", id, "err", err)
			rl.Close()
			lock.Unlock()
			continue
		}
		r.recovering = true
		e.launch(r)
		n++
	}
	return n, nil
}

// Shutdown stops every run goroutine. Running agent/run visits are killed
// and recorded as interrupted; asks, waits and fanouts resume on restart.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.stop()
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until every run goroutine has exited.
func (e *Engine) Wait() { e.wg.Wait() }
