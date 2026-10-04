// Package proc owns spawning and killing child processes: process groups,
// tee'd output, graceful termination and the login-shell environment.
package proc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultGrace is how long a process group gets between SIGTERM and SIGKILL.
const DefaultGrace = 10 * time.Second

// Spec describes a process to run.
type Spec struct {
	Path   string
	Args   []string // without argv[0]
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Grace overrides DefaultGrace.
	Grace time.Duration
}

// Result is how a process ended.
type Result struct {
	ExitCode int  // -1 if it didn't exit normally
	Signaled bool // killed by a signal (including our own cancellation)
}

// ErrStart wraps failures to start the process.
type ErrStart struct{ Err error }

func (e *ErrStart) Error() string { return "failed to start: " + e.Err.Error() }
func (e *ErrStart) Unwrap() error { return e.Err }

// Run starts the process in its own process group and waits for it. When
// ctx ends, the whole group gets SIGTERM, then SIGKILL after Grace. The
// returned error is non-nil only when the process couldn't start or waiting
// failed for a reason other than its exit status.
func Run(ctx context.Context, s Spec) (Result, error) {
	cmd := exec.Command(s.Path, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	cmd.Stdin = s.Stdin
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	setGroup(cmd)
	// Grandchildren may keep pipes open after the group is killed; don't
	// wait for them forever.
	grace := s.Grace
	if grace <= 0 {
		grace = DefaultGrace
	}
	cmd.WaitDelay = grace + 2*time.Second

	if err := cmd.Start(); err != nil {
		return Result{ExitCode: -1}, &ErrStart{err}
	}
	done := make(chan struct{})
	var killOnce sync.Once
	go func() {
		select {
		case <-ctx.Done():
			killOnce.Do(func() { terminateGroup(cmd.Process.Pid, grace, done) })
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	res := Result{ExitCode: cmd.ProcessState.ExitCode(), Signaled: exitSignaled(cmd.ProcessState)}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) || errors.Is(err, exec.ErrWaitDelay) {
			return res, nil
		}
		return res, err
	}
	return res, nil
}

// Tail keeps the last N lines written to it. It is safe for concurrent use.
type Tail struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial bytes.Buffer
}

// NewTail returns a Tail keeping n lines.
func NewTail(n int) *Tail { return &Tail{max: n} }

// Write implements io.Writer.
func (t *Tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.partial.Write(p)
	for {
		b := t.partial.Bytes()
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		t.push(string(b[:i]))
		t.partial.Next(i + 1)
	}
	return len(p), nil
}

func (t *Tail) push(l string) {
	if t.max <= 0 {
		return
	}
	t.lines = append(t.lines, l)
	if len(t.lines) > t.max {
		t.lines = append([]string(nil), t.lines[len(t.lines)-t.max:]...)
	}
}

// Lines returns the kept lines, including an unterminated last line.
func (t *Tail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]string(nil), t.lines...)
	if t.partial.Len() > 0 {
		out = append(out, t.partial.String())
		if t.max > 0 && len(out) > t.max {
			out = out[len(out)-t.max:]
		}
	}
	return out
}

// LastNonEmpty returns the last non-blank line.
func (t *Tail) LastNonEmpty() string {
	lines := t.Lines()
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}

// FuncWriter adapts a function to io.Writer.
type FuncWriter func(p []byte)

// Write implements io.Writer.
func (f FuncWriter) Write(p []byte) (int, error) {
	f(append([]byte(nil), p...))
	return len(p), nil
}

// MergeEnv merges KEY=VALUE lists; later lists win.
func MergeEnv(lists ...[]string) []string {
	m := map[string]string{}
	var order []string
	for _, l := range lists {
		for _, kv := range l {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				continue
			}
			if _, seen := m[k]; !seen {
				order = append(order, k)
			}
			m[k] = v
		}
	}
	sort.Strings(order)
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+m[k])
	}
	return out
}

// LoginEnv captures the user's login-shell environment (`$SHELL -lc env`)
// so PATH includes brew, asdf and friends. It falls back to the current
// environment.
func LoginEnv(ctx context.Context) []string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	res, err := Run(ctx, Spec{Path: shell, Args: []string{"-lc", "/usr/bin/env -0"}, Env: os.Environ(), Stdout: &out, Stderr: io.Discard})
	if err != nil || res.ExitCode != 0 {
		return os.Environ()
	}
	var login []string
	for _, kv := range strings.Split(out.String(), "\x00") {
		if strings.Contains(kv, "=") {
			login = append(login, kv)
		}
	}
	// Login values win (PATH), but keep anything only the daemon has.
	return MergeEnv(os.Environ(), login)
}
