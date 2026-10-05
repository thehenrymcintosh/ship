package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/agent/fake"
	"github.com/thehenrymcintosh/ship/internal/config"
	"github.com/thehenrymcintosh/ship/internal/store"
)

type env struct {
	t    *testing.T
	repo string
	home string
	st   *store.Store
	e    *Engine
	cfg  config.Config
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newEnv makes a git repo with the given pipelines and an engine.
func newEnv(t *testing.T, pipelines map[string]string) *env {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".ship", "pipelines"), 0o755)
	for name, src := range pipelines {
		os.WriteFile(filepath.Join(repo, ".ship", "pipelines", name+".yml"), []byte(src), 0o644)
	}
	gitRun(t, repo, "init", "-q", "-b", "main")
	gitRun(t, repo, "config", "user.email", "t@example.com")
	gitRun(t, repo, "config", "user.name", "t")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "init")
	en := &env{t: t, repo: repo, home: filepath.Join(root, "home")}
	en.cfg = config.Defaults()
	en.cfg.Workspace.Fetch = false
	en.cfg.Workspace.Provider = "git" // not auto: results mustn't depend on treehouse being installed
	en.cfg.Workspace.Git.Dir = filepath.Join(root, "worktrees", "{run}")
	en.st, _ = store.New(filepath.Join(en.home, "state"))
	en.e = en.newEngine()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		en.e.Shutdown(ctx)
	})
	return en
}

func (en *env) newEngine() *Engine {
	reg := agent.NewRegistry()
	reg.Register(fake.New())
	return New(Options{
		Store: en.st, Agents: reg,
		LoadConfig:      func(string) (config.Config, error) { return en.cfg, nil },
		MaxAgents:       3,
		GlobalPipelines: GlobalPipelinesDir(en.home),
	})
}

func (en *env) start(pipeline, briefText string, vars map[string]string, fakeScript string) *store.RunSnapshot {
	en.t.Helper()
	if briefText == "" {
		briefText = "---\ntitle: Test run\nacceptance: [works]\n---\nbody\n"
	}
	script := ""
	if fakeScript != "" {
		script = filepath.Join(en.home, "fake-"+pipeline+".yml")
		os.MkdirAll(en.home, 0o755)
		os.WriteFile(script, []byte(fakeScript), 0o644)
	}
	snap, err := en.e.Start(context.Background(), StartRequest{Repo: en.repo, Pipeline: pipeline, Brief: []byte(briefText), Vars: vars, FakeAgents: script})
	if err != nil {
		if e, ok := err.(*Error); ok {
			en.t.Fatalf("start: %v %v", err, e.Findings)
		}
		en.t.Fatalf("start: %v", err)
	}
	return snap
}

// waitFor polls until cond holds.
func (en *env) waitFor(id string, what string, cond func(*store.RunSnapshot) bool) *store.RunSnapshot {
	en.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var s *store.RunSnapshot
	for time.Now().Before(deadline) {
		var err error
		s, err = en.e.Snapshot(id)
		if err == nil && cond(s) {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	var kids []string
	if s != nil {
		for _, c := range s.Children {
			if cs, err := en.e.Snapshot(c.ID); err == nil {
				kids = append(kids, fmt.Sprintf("\n  child %s: %s %q %s", c.ID, cs.Status, cs.StatusReason, visitTrail(cs)))
			}
		}
	}
	var evs []string
	if all, _, err := store.ReadEvents(en.st.RunDir(id)); err == nil {
		for _, e := range all[max(0, len(all)-15):] {
			evs = append(evs, e.Type+" "+string(e.Data))
		}
	}
	en.t.Fatalf("timed out waiting for %s; status=%s reason=%q step=%s visits=%s%s\nlast events:\n  %s", what, s.Status, s.StatusReason, s.CurrentStep, visitTrail(s), strings.Join(kids, ""), strings.Join(evs, "\n  "))
	return nil
}

func (en *env) waitStatus(id string, st store.Status) *store.RunSnapshot {
	return en.waitFor(id, string(st), func(s *store.RunSnapshot) bool { return s.Status == st })
}

func visitTrail(s *store.RunSnapshot) string {
	if s == nil {
		return ""
	}
	var parts []string
	for _, v := range s.Visits {
		parts = append(parts, v.Step+":"+v.Outcome)
	}
	return strings.Join(parts, " ")
}

const loopPipeline = `version: 1
start: build
variables:
  greeting: { value: hello world }
  out: { set_by: build }
defaults:
  max_visits: 2
  when_exhausted: check-in
workspace:
  provider: none
steps:
  build:
    run: |
      echo "$SHIP_VISIT_SEQ" >> count.txt
      echo {{raw vars.greeting}}
    save:
      out: last_line
    next:
      pass: check
      fail: check-in
  check:
    run: test "$(wc -l < count.txt)" -ge 3
    next:
      pass: done
      fail: build
  check-in:
    ask: "Stuck at {{came_from}}. What next?"
    input: optional
    choices:
      retry: $came_from
      abandon: stop
`

func TestRunAskLoopWithExhaustion(t *testing.T) {
	en := newEnv(t, map[string]string{"loop": loopPipeline})
	snap := en.start("loop", "", nil, "")
	id := snap.ID

	s := en.waitStatus(id, store.StatusAsking)
	if got := visitTrail(s); got != "build:pass check:fail build:pass check:fail check-in:" {
		t.Fatalf("trail %q", got)
	}
	if s.PendingAsk == nil || s.PendingAsk.Question != "Stuck at build. What next?" || s.Vars["out"] != "hello world" {
		t.Fatalf("ask %+v vars %v", s.PendingAsk, s.Vars)
	}
	if err := en.e.Do(id, Command{Name: CmdAnswer, Choice: "nope"}); KindOf(err) != KindInvalid {
		t.Fatalf("bad choice should be invalid, got %v", err)
	}
	// retry → $came_from = build, with build's counter reset.
	if err := en.e.Do(id, Command{Name: CmdAnswer, Choice: "retry", Note: "try once more"}); err != nil {
		t.Fatal(err)
	}
	// build(3) → check is exhausted now → check-in, came_from = check.
	s = en.waitFor(id, "second ask", func(s *store.RunSnapshot) bool { return s.Status == store.StatusAsking && len(s.Visits) > 5 })
	if s.PendingAsk.Question != "Stuck at check. What next?" {
		t.Fatalf("question %q trail %s", s.PendingAsk.Question, visitTrail(s))
	}
	if err := en.e.Do(id, Command{Name: CmdAnswer, Choice: "retry"}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(id, store.StatusDone)
	if got := visitTrail(s); got != "build:pass check:fail build:pass check:fail check-in:retry build:pass check-in:retry check:pass" {
		t.Fatalf("trail %q", got)
	}
	// The handover of the ask carries the note.
	h, _ := os.ReadFile(filepath.Join(en.st.RunDir(id), "visits", s.Visits[4].Dir, "handover.md"))
	if !strings.Contains(string(h), "try once more") {
		t.Fatalf("handover:\n%s", h)
	}
	// Replay equals the live snapshot.
	rebuilt, _, err := store.Rebuild(en.st.RunDir(id))
	if err != nil || rebuilt.LastEventSeq != s.LastEventSeq || rebuilt.Status != store.StatusDone {
		t.Fatalf("rebuild %v %+v", err, rebuilt)
	}
}

const agentPipeline = `version: 1
start: implement
defaults:
  on_error: check-in
steps:
  implement:
    agent: /implement
    session: continue
    next: review
  review:
    agent: /review mode=code
    model: opus
    next:
      pass: done
      changes: implement
      stuck: check-in
  check-in:
    ask: "stopped at {{came_from}}"
    choices:
      retry: $came_from
      abandon: stop
`

const agentScript = `implement:
  - {outcome: done, summary: "wrote code", cost: 0.10, write: {"impl.txt": "v1"}, run: "git add -A && git commit -qm impl"}
  - {outcome: done, summary: "added tests", cost: 0.05}
review:
  - {outcome: changes, summary: "missing tests", cost: 0.20}
  - {outcome: pass, summary: "lgtm", cost: 0.20, invalid_attempts: 1}
`

func TestAgentLoopWithFake(t *testing.T) {
	en := newEnv(t, map[string]string{"feat": agentPipeline})
	s := en.start("feat", "", nil, agentScript)
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "implement:done review:changes implement:done review:pass" {
		t.Fatalf("trail %q", got)
	}
	// 0.10 + 0.20 + 0.05 + 2×0.20 (the corrective retry is billed too).
	if s.CostUSD < 0.749 || s.CostUSD > 0.751 {
		t.Errorf("cost %v", s.CostUSD)
	}
	// session: continue resumes implement's first session.
	if s.Visits[2].ResumeID == "" || s.Visits[2].ResumeID != s.Visits[0].SessionID {
		t.Errorf("continue: %+v vs %+v", s.Visits[2], s.Visits[0])
	}
	if s.Visits[1].SessionID == "" || s.Visits[3].ResumeID != "" {
		t.Errorf("review should be fresh each time: %+v", s.Visits[3])
	}
	// The agent's commit is on the run branch, and the worktree was released.
	if !strings.Contains(gitRun(t, en.repo, "log", "--oneline", s.Branch), "impl") {
		t.Error("agent commit missing from branch")
	}
	if s.Workspace != nil {
		t.Error("worktree should be released on done")
	}
	if !strings.HasPrefix(s.Branch, "ship/") {
		t.Errorf("branch %q", s.Branch)
	}
	// The review prompt preamble lists outcomes and the handover.
	in, _ := os.ReadFile(filepath.Join(en.st.RunDir(s.ID), "visits", s.Visits[3].Dir, "input.md"))
	for _, want := range []string{`"changes" → next: implement (agent)`, `"stuck" → next: a human check-in`, "added tests", `choose the outcome that leads to a human ("stuck")`} {
		if !strings.Contains(string(in), want) {
			t.Errorf("preamble missing %q:\n%s", want, in)
		}
	}
	// Correction retry happened for the last review (2 result lines in its transcript).
	tr, _ := os.ReadFile(filepath.Join(en.st.RunDir(s.ID), "visits", s.Visits[3].Dir, "transcript.jsonl"))
	if strings.Count(string(tr), `"type":"result"`) != 2 {
		t.Errorf("want a corrective retry:\n%s", tr)
	}
}

func TestErrorParksAndManualControls(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: a
workspace: {provider: none}
variables:
  url: { set_by: c }
steps:
  a:
    run: exit 0
    next: b
  b:
    run: echo "{{vars.url}}"
    next: c
  c:
    run: echo saved
    save: {url: last_line}
    next: done
`})
	s := en.start("p", "", nil, "")
	// b references url before it's set → error → no on_error → parked.
	s = en.waitStatus(s.ID, store.StatusNeedsAttention)
	if !strings.Contains(s.StatusReason, "unset_var:url") {
		t.Fatalf("reason %q", s.StatusReason)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdSetVar, Var: "url", Value: "https://x"}); err != nil {
		t.Fatal(err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "x"}); KindOf(err) != KindConflict {
		t.Fatalf("answer while parked: %v", err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdRetry}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "a:pass b:error b:pass c:pass" || s.Vars["url"] != "saved" {
		t.Fatalf("trail %q", got)
	}
}

func TestGotoAndCancel(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: slow
workspace: {provider: none}
steps:
  slow:
    run: sleep 30
    next: done
  quick:
    run: "true"
    next: slow
`})
	s := en.start("p", "", nil, "")
	en.waitFor(s.ID, "slow running", func(s *store.RunSnapshot) bool { return len(s.Visits) == 1 })
	if err := en.e.Do(s.ID, Command{Name: CmdGoto, Step: "quick"}); err != nil {
		t.Fatal(err)
	}
	s2 := en.waitFor(s.ID, "slow again", func(s *store.RunSnapshot) bool { return len(s.Visits) == 3 })
	if got := visitTrail(s2); got != "slow:cancelled quick:pass slow:" {
		t.Fatalf("trail %q", got)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdCancel}); err != nil {
		t.Fatal(err)
	}
	s2 = en.waitStatus(s.ID, store.StatusCancelled)
	if s2.Visits[2].Outcome != "cancelled" {
		t.Fatalf("%+v", s2.Visits[2])
	}
}

func TestStartValidation(t *testing.T) {
	en := newEnv(t, map[string]string{
		"bad": "version: 1\nstart: nowhere\nsteps:\n  a: {run: 'true'}\n",
		"v": `version: 1
start: a
workspace: {provider: none}
variables:
  ticket: {from_brief: true, format: '^[A-Z]+-[0-9]+$'}
steps:
  a: {run: 'echo {{vars.ticket}}'}
`})
	_, err := en.e.Start(context.Background(), StartRequest{Repo: en.repo, Pipeline: "bad", Brief: []byte("---\ntitle: x\n---\n")})
	if e, ok := err.(*Error); !ok || len(e.Findings) == 0 || e.Findings[0].Code != "E004" {
		t.Fatalf("want E004 findings, got %v", err)
	}
	_, err = en.e.Start(context.Background(), StartRequest{Repo: en.repo, Pipeline: "v", Brief: []byte("---\ntitle: x\n---\n")})
	if err == nil || !strings.Contains(err.Error(), `missing variable "ticket"`) {
		t.Fatalf("want missing var, got %v", err)
	}
	_, err = en.e.Start(context.Background(), StartRequest{Repo: en.repo, Pipeline: "v", Brief: []byte("---\ntitle: x\nvars: {ticket: nope}\n---\n")})
	if err == nil || !strings.Contains(err.Error(), "doesn't match format") {
		t.Fatalf("want format error, got %v", err)
	}
	s := en.start("v", "---\ntitle: x\nvars: {ticket: API-1}\n---\n", nil, "")
	s = en.waitStatus(s.ID, store.StatusDone)
	// Only one `none` run per repo at a time is enforced for active runs.
	_ = s
}

func TestMaxTransitions(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: a
workspace: {provider: none}
limits: {max_transitions: 5}
defaults: {max_visits: 0}
steps:
  a: {run: "true", next: b}
  b: {run: "true", next: a}
`})
	s := en.start("p", "", nil, "")
	s = en.waitStatus(s.ID, store.StatusFailed)
	if s.StatusReason != "max_transitions" || s.Transitions != 5 {
		t.Fatalf("%s %d", s.StatusReason, s.Transitions)
	}
}

func TestAskVariable(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: a
workspace: {provider: none}
variables:
  name: {ask: "Who?", format: '^[a-z]+$'}
steps:
  a:
    run: echo {{vars.name}}
    next: done
`})
	s := en.start("p", "", nil, "")
	s = en.waitStatus(s.ID, store.StatusAsking)
	if s.PendingAsk == nil || s.PendingAsk.Kind != store.AskKindVar || s.PendingAsk.Question != "Who?" || len(s.Visits) != 0 {
		t.Fatalf("%+v", s.PendingAsk)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Note: "Bob"}); KindOf(err) != KindInvalid {
		t.Fatalf("format should reject: %v", err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Note: "bob"}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	if s.Vars["name"] != "bob" {
		t.Fatal(s.Vars)
	}
}

func TestBudget(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: a
limits: {max_budget_usd: 0.5}
defaults: {max_visits: 0}
steps:
  a:
    agent: /x
    next: {again: a, stuck: check-in}
  check-in:
    ask: over budget
    choices: {stop: stop}
`})
	s := en.start("p", "", nil, "a: [{outcome: again, summary: s, cost: 0.3}]\n")
	// The run's budget holds the run for the person; on_error isn't used.
	s = en.waitStatus(s.ID, store.StatusNeedsAttention)
	lv := s.LastVisit()
	if !strings.HasPrefix(s.StatusReason, "budget reached") || visitTrail(s) != "a:again a:error" ||
		lv.Error == nil || lv.Error.Reason != "run_budget" {
		t.Fatalf("%q %s %+v", s.StatusReason, visitTrail(s), lv)
	}
}

// recorder captures published events.
type recorder struct {
	mu  sync.Mutex
	out []string
}

func (r *recorder) RunEvent(*store.RunSnapshot, store.Event) {}
func (r *recorder) Output(id string, seq int, stream string, chunk []byte) {
	r.mu.Lock()
	r.out = append(r.out, fmt.Sprintf("%d:%s:%s", seq, stream, chunk))
	r.mu.Unlock()
}
func (r *recorder) Agent(string, int, agent.UIEvent) {}

func TestOutputPublished(t *testing.T) {
	en := newEnv(t, map[string]string{"p": "version: 1\nstart: a\nworkspace: {provider: none}\nsteps:\n  a: {run: 'echo hi; echo err >&2'}\n"})
	rec := &recorder{}
	en.e.o.Publisher = rec
	s := en.start("p", "", nil, "")
	en.waitStatus(s.ID, store.StatusDone)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	got := strings.Join(rec.out, "|")
	if !strings.Contains(got, "1:stdout:hi\n") || !strings.Contains(got, "1:stderr:err\n") {
		t.Fatal(got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalPipelines(t *testing.T) {
	en := newEnv(t, map[string]string{
		"shared": "version: 1\nstart: a\nworkspace: {provider: none}\nsteps:\n  a: {run: 'echo repo', save: {who: last_line}}\nvariables:\n  who: {set_by: a}\n",
	})
	global := GlobalPipelinesDir(en.home)
	os.MkdirAll(filepath.Join(en.home, "bin"), 0o755)
	writeFile(t, filepath.Join(en.home, "bin", "hello"), "#!/usr/bin/env bash\necho from-global-bin\n")
	os.Chmod(filepath.Join(en.home, "bin", "hello"), 0o755)
	os.MkdirAll(global, 0o755)
	writeFile(t, filepath.Join(global, "lint.yml"), `version: 1
start: a
workspace: {provider: none}
variables:
  who: {set_by: a}
steps:
  a:
    run: '"$SHIP_HOME/bin/hello"'
    save: {who: last_line}
`)
	writeFile(t, filepath.Join(global, "shared.yml"), "version: 1\nstart: a\nsteps:\n  a: {run: 'echo global'}\n")

	if names := en.e.Loader(en.repo).Names(); strings.Join(names, ",") != "lint,shared" {
		t.Fatalf("names %v", names)
	}
	s := en.start("lint", "", nil, "")
	s = en.waitStatus(s.ID, store.StatusDone)
	if s.Vars["who"] != "from-global-bin" {
		t.Fatalf("global pipeline: %v", s.Vars)
	}
	// The repo's pipeline shadows the global one of the same name.
	s = en.start("shared", "", nil, "")
	s = en.waitStatus(s.ID, store.StatusDone)
	if s.Vars["who"] != "repo" {
		t.Fatalf("shadowing: %v", s.Vars)
	}
	// The run snapshotted the global file it used.
	if _, err := os.Stat(filepath.Join(en.st.RunDir(s.ID), "pipeline", "shared.yml")); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyPromptGetsDefault(t *testing.T) {
	en := newEnv(t, map[string]string{"p": "version: 1\nstart: s\nsteps:\n  s:\n    split: \"\"\n    next: {ok: done}\n"})
	s := en.start("p", "", nil, "s: [{outcome: ok, summary: one, slices: [{key: a, title: A, brief: b, acceptance: [x]}]}]\n")
	s = en.waitStatus(s.ID, store.StatusDone)
	in, _ := os.ReadFile(filepath.Join(en.st.RunDir(s.ID), "visits", s.Visits[0].Dir, "input.md"))
	if !strings.Contains(string(in), "Do the work for this step") {
		t.Fatalf("empty prompt should get a default:\n%s", in)
	}
}

func TestNamedSessions(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: domain
defaults: {max_visits: 0}
steps:
  domain:
    prompt: Design the domain model.
    session: design
    next: review
  review:
    prompt: Review the domain model.
    next: interface
  interface:
    prompt: Design the interfaces.
    session: design
    next: verify
  verify:
    prompt: Verify everything.
    session: continue
    next: {pass: done, again: fix}
  fix:
    prompt: Fix what verify found.
    next: verify
`})
	commit := "run: 'echo \"$SHIP_STEP $SHIP_VISIT_SEQ\" >> log.txt && git add -A && git commit -qm \"$SHIP_STEP $SHIP_VISIT_SEQ\"'"
	s := en.start("p", "", nil, `domain: [{outcome: done, summary: "modelled orders", `+commit+`}]
review: [{outcome: done, summary: "domain looks right", `+commit+`}]
interface: [{outcome: done, summary: "designed the API"}]
verify:
  - {outcome: again, summary: "missing a test"}
  - {outcome: pass, summary: "all good"}
fix: [{outcome: done, summary: "added the test", `+commit+`}]
`)
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "domain:done review:done interface:done verify:again fix:done verify:pass" {
		t.Fatal(got)
	}
	v := s.Visits
	// interface resumes domain's conversation; review is fresh in between.
	if v[0].Thread != "design" || v[2].Thread != "design" || v[2].ResumeID != v[0].SessionID || v[1].ResumeID != "" || v[1].Thread != "" {
		t.Fatalf("design thread: %+v | %+v | %+v", v[0], v[1], v[2])
	}
	// verify continues its own conversation on its second visit.
	if v[3].Thread != "verify" || v[5].ResumeID != v[3].SessionID {
		t.Fatalf("verify thread: %+v %+v", v[3], v[5])
	}
	if v[0].HeadSHA == "" {
		t.Fatal("visits should record the worktree HEAD")
	}
	input := func(i int) string {
		b, _ := os.ReadFile(filepath.Join(en.st.RunDir(s.ID), "visits", v[i].Dir, "input.md"))
		return string(b)
	}
	in := input(2)
	for _, want := range []string{`now as step "interface"`, "Design the interfaces.", "- review → done: domain looks right", "Commits since then:", "review 2"} {
		if !strings.Contains(in, want) {
			t.Errorf("interface prompt missing %q:\n%s", want, in)
		}
	}
	in = input(5)
	for _, want := range []string{`back at step "verify" (visit 2)`, "- fix → done: added the test", "fix 5", "log.txt"} {
		if !strings.Contains(in, want) {
			t.Errorf("verify revisit prompt missing %q:\n%s", want, in)
		}
	}
}

func TestWarnsAboutFilesTheRunWontSee(t *testing.T) {
	en := newEnv(t, map[string]string{"p": "version: 1\nstart: a\nsteps:\n  a: {agent: /my-skill, next: done}\n"})
	skill := filepath.Join(en.repo, ".claude", "skills", "my-skill", "SKILL.md")
	os.MkdirAll(filepath.Dir(skill), 0o755)
	writeFile(t, skill, "---\nname: my-skill\n---\nv1\n")
	script := "a: [{outcome: done, summary: ok}]\n"
	s := en.start("p", "", nil, script)
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "skill .claude/skills/my-skill isn't committed on main") {
		t.Fatalf("untracked: %v", s.Warnings)
	}
	en.waitStatus(s.ID, store.StatusDone)
	gitRun(t, en.repo, "add", "-A")
	gitRun(t, en.repo, "commit", "-qm", "skill")
	if s := en.start("p", "", nil, script); len(s.Warnings) != 0 {
		t.Fatalf("committed: %v", s.Warnings)
	}
	writeFile(t, skill, "---\nname: my-skill\n---\nv2\n")
	if s := en.start("p", "", nil, script); len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "differs from main") {
		t.Fatalf("modified: %v", s.Warnings)
	}
}

func TestNoWarningWhenTheRunsBranchHasTheFiles(t *testing.T) {
	en := newEnv(t, map[string]string{"p": "version: 1\nstart: a\nworkspace: {branch: audit}\nsteps:\n  a: {agent: /my-skill, next: done}\n"})
	// The skill is committed on the run's existing branch, not on main.
	gitRun(t, en.repo, "checkout", "-q", "-b", "audit")
	skill := filepath.Join(en.repo, ".claude", "skills", "my-skill", "SKILL.md")
	os.MkdirAll(filepath.Dir(skill), 0o755)
	writeFile(t, skill, "---\nname: my-skill\n---\nv1\n")
	gitRun(t, en.repo, "add", "-A")
	gitRun(t, en.repo, "commit", "-qm", "setup")
	gitRun(t, en.repo, "checkout", "-q", "main")
	os.MkdirAll(filepath.Dir(skill), 0o755)
	writeFile(t, skill, "---\nname: my-skill\n---\nv1\n") // untracked copy on main
	s := en.start("p", "", nil, "a: [{outcome: done, summary: ok}]\n")
	if len(s.Warnings) != 0 {
		t.Fatalf("warned although the branch has the skill: %v", s.Warnings)
	}
}
