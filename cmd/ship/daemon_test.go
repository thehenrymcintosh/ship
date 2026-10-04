package main

// Daemon integration tests: a real `ship` binary, a temp repo and
// home, driven over the HTTP API.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/merlin-digital/ship/internal/daemon"
	"github.com/merlin-digital/ship/internal/store"
)

var shipBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ship-bin")
	if err != nil {
		panic(err)
	}
	shipBin = filepath.Join(dir, "ship")
	if out, err := exec.Command("go", "build", "-o", shipBin, ".").CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build: %v\n%s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type harness struct {
	t    *testing.T
	home string
	repo string
	srv  *exec.Cmd
	c    *daemon.Client
}

func newHarness(t *testing.T, pipelines map[string]string) *harness {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	h := &harness{t: t, home: filepath.Join(root, "home"), repo: filepath.Join(root, "repo")}
	os.MkdirAll(filepath.Join(h.repo, ".ship", "pipelines"), 0o755)
	for n, src := range pipelines {
		os.WriteFile(filepath.Join(h.repo, ".ship", "pipelines", n+".yml"), []byte(src), 0o644)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@e"}, {"config", "user.name", "t"}, {"add", "."}, {"commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", h.repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	os.MkdirAll(h.home, 0o700)
	os.WriteFile(filepath.Join(h.home, "config.yml"), []byte("notifications: false\nworkspace:\n  fetch: false\n  git:\n    dir: \""+root+"/wt/{run}\"\n"), 0o644)
	h.startDaemon()
	t.Cleanup(h.stopDaemon)
	return h
}

func (h *harness) startDaemon() {
	h.t.Helper()
	h.srv = exec.Command(shipBin, "serve", "--home", h.home, "--port", "0")
	logf, _ := os.OpenFile(filepath.Join(h.home, "test-daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	h.srv.Stdout, h.srv.Stderr = logf, logf
	if err := h.srv.Start(); err != nil {
		h.t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := daemon.Connect(h.home)
		if err == nil && c.Info.PID == h.srv.Process.Pid {
			h.c = c
			return
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(h.home, "test-daemon.log"))
			h.t.Fatalf("daemon didn't start: %v\n%s", err, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *harness) stopDaemon() {
	if h.srv != nil && h.srv.ProcessState == nil {
		h.srv.Process.Signal(syscall.SIGTERM)
		h.srv.Wait()
	}
}

func (h *harness) kill9() {
	h.srv.Process.Kill()
	h.srv.Wait()
}

func (h *harness) startRun(pipeline, fakeScript string) string {
	h.t.Helper()
	script := filepath.Join(h.home, "fake.yml")
	os.WriteFile(script, []byte(fakeScript), 0o644)
	var res struct{ ID string }
	err := h.c.Do("POST", "/api/runs", daemon.StartBody{Repo: h.repo, Pipeline: pipeline, Brief: "---\ntitle: Daemon test\n---\n", FakeAgents: script}, &res)
	if err != nil {
		h.t.Fatal(err)
	}
	return res.ID
}

func (h *harness) waitFor(id string, cond func(*store.RunSnapshot) bool) *store.RunSnapshot {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var s store.RunSnapshot
	for time.Now().Before(deadline) {
		if err := h.c.Do("GET", "/api/runs/"+id, nil, &s); err == nil && cond(&s) {
			return &s
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("timed out; status=%s reason=%q step=%s", s.Status, s.StatusReason, s.CurrentStep)
	return nil
}

const askPipeline = `version: 1
start: work
steps:
  work:
    agent: /work
    next: {done: chef}
  chef:
    ask: "Ship it?"
    choices: {yes: done, no: stop}
`

func TestDaemonAPIAndSSE(t *testing.T) {
	h := newHarness(t, map[string]string{"p": askPipeline})

	// Subscribe to the event stream before starting.
	req, _ := http.NewRequest("GET", h.c.Info.URL()+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+h.c.Info.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: ") {
				events <- strings.TrimPrefix(sc.Text(), "event: ")
			}
		}
	}()

	id := h.startRun("p", "work: [{outcome: done, summary: did it}]\n")
	s := h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusAsking })
	if s.PendingAsk.Question != "Ship it?" {
		t.Fatal(s.PendingAsk)
	}
	sawRun, sawInbox := false, false
	timeout := time.After(5 * time.Second)
	for !(sawRun && sawInbox) {
		select {
		case e := <-events:
			sawRun = sawRun || e == "run"
			sawInbox = sawInbox || e == "inbox"
		case <-timeout:
			t.Fatalf("SSE: run=%v inbox=%v", sawRun, sawInbox)
		}
	}

	var inbox []map[string]any
	h.c.Do("GET", "/api/inbox", nil, &inbox)
	if len(inbox) != 1 {
		t.Fatalf("inbox %v", inbox)
	}
	// Wrong state → 409; bad choice → 422.
	if err := h.c.Do("POST", "/api/runs/"+id+"/resume-session", nil, nil); exitCode(err) != exitConflict {
		t.Fatalf("want conflict, got %v", err)
	}
	if err := h.c.Do("POST", "/api/runs/"+id+"/answer", map[string]string{"choice": "maybe"}, nil); err == nil || !strings.Contains(err.Error(), "isn't a choice") {
		t.Fatalf("want invalid choice, got %v", err)
	}
	// The CLI answers through the API.
	out, err := exec.Command(shipBin, "--home", h.home, "answer", id[len(id)-4:], "yes", "--note", "ship it").CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	s = h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusDone })
	if s.Visits[1].Summary != "ship it" {
		t.Fatalf("note not recorded: %+v", s.Visits[1])
	}
	// Exit codes: unknown run → 3.
	cmd := exec.Command(shipBin, "--home", h.home, "status", "nope-nope")
	if err := cmd.Run(); cmd.ProcessState.ExitCode() != exitNotFound {
		t.Fatalf("exit %d (%v)", cmd.ProcessState.ExitCode(), err)
	}
}

func TestDaemonKill9MidAgent(t *testing.T) {
	h := newHarness(t, map[string]string{"p": askPipeline})
	id := h.startRun("p", "work:\n  - {outcome: done, summary: slow, sleep: 60s}\n  - {outcome: done, summary: resumed}\n")
	h.waitFor(id, func(s *store.RunSnapshot) bool { return len(s.Visits) == 1 && !s.Visits[0].Queued })
	h.kill9()
	h.startDaemon()
	s := h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusNeedsAttention })
	if lv := s.LastVisit(); !lv.Interrupted || lv.SessionID == "" {
		t.Fatalf("want interrupted visit with a session: %+v", lv)
	}
	if err := h.c.Do("POST", "/api/runs/"+id+"/resume-session", nil, nil); err != nil {
		t.Fatal(err)
	}
	s = h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusAsking })
	if s.Visits[1].ResumeID != s.Visits[0].SessionID {
		t.Fatalf("resume visit %+v", s.Visits[1])
	}
	var ev []json.RawMessage
	h.c.Do("GET", "/api/runs/"+id+"/events?after=0", nil, &ev)
	if len(ev) == 0 {
		t.Fatal("no events")
	}
}
