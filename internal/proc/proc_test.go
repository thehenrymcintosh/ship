package proc

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunExitAndTee(t *testing.T) {
	var out bytes.Buffer
	tail := NewTail(2)
	res, err := Run(context.Background(), Spec{Path: "bash", Args: []string{"-c", "echo a; echo b; echo c; exit 3"}, Env: os.Environ(), Stdout: ioMulti(&out, tail)})
	if err != nil || res.ExitCode != 3 {
		t.Fatal(res, err)
	}
	if out.String() != "a\nb\nc\n" || strings.Join(tail.Lines(), ",") != "b,c" || tail.LastNonEmpty() != "c" {
		t.Fatalf("%q %v", out.String(), tail.Lines())
	}
}

func TestStartFailure(t *testing.T) {
	_, err := Run(context.Background(), Spec{Path: "/nonexistent/x"})
	if _, ok := err.(*ErrStart); !ok {
		t.Fatalf("want ErrStart, got %v", err)
	}
}

func TestCancelKillsGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	// The child ignores SIGTERM and has a grandchild holding stdout open.
	res, err := Run(ctx, Spec{Path: "bash", Args: []string{"-c", "trap '' TERM; sleep 30 & sleep 30"}, Env: os.Environ(), Stdout: &bytes.Buffer{}, Grace: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Signaled || time.Since(start) > 5*time.Second {
		t.Fatalf("res %+v after %v", res, time.Since(start))
	}
}

func TestMergeEnv(t *testing.T) {
	got := MergeEnv([]string{"A=1", "B=2"}, []string{"B=3", "C=4", "bad"})
	if strings.Join(got, " ") != "A=1 B=3 C=4" {
		t.Fatal(got)
	}
}

func ioMulti(a, b interface{ Write([]byte) (int, error) }) *multi { return &multi{a, b} }

type multi struct {
	a, b interface{ Write([]byte) (int, error) }
}

func (m *multi) Write(p []byte) (int, error) { m.a.Write(p); return m.b.Write(p) }

func TestJoinPaths(t *testing.T) {
	if got := JoinPaths("/venv/bin:/usr/bin", "/opt/homebrew/bin:/usr/bin"); got != "/venv/bin:/usr/bin:/opt/homebrew/bin" {
		t.Fatal(got)
	}
}
