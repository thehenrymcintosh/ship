package store

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thehenrymcintosh/ship/internal/workspace"
)

// randomEvent produces a plausible event of a random type.
func randomEvent(rng *rand.Rand, seq int) (string, any) {
	steps := []string{"implement", "review", "gate", "chef"}
	step := steps[rng.Intn(len(steps))]
	switch rng.Intn(16) {
	case 0:
		return EvVisitStarted, VisitStarted{Seq: seq, Step: step, Type: "run", VisitNumber: rng.Intn(5) + 1, CameFrom: steps[rng.Intn(4)], Queued: rng.Intn(2) == 0, Dir: fmt.Sprintf("%04d-%s", seq, step)}
	case 1:
		return EvVisitFinished, VisitFinished{Seq: rng.Intn(seq + 1), Outcome: "pass", Summary: "did things", CostUSD: rng.Float64(), DurationMS: int64(rng.Intn(1000)), PermissionDenials: rng.Intn(2)}
	case 2:
		return EvTransition, Transition{From: step, To: steps[rng.Intn(4)], Outcome: "pass", Reason: ReasonNormal, Reset: rng.Intn(3) == 0}
	case 3:
		return EvVarSet, VarSet{Name: fmt.Sprintf("v%d", rng.Intn(3)), Value: fmt.Sprint(rng.Int())}
	case 4:
		return EvAskPending, AskPending{Seq: rng.Intn(seq + 1), Kind: AskKindAsk, Question: "q?", Choices: []string{"a", "b"}, Input: "optional"}
	case 5:
		return EvAskAnswered, AskAnswered{Seq: rng.Intn(seq + 1), Choice: "a"}
	case 6:
		return EvStatusChanged, StatusChanged{To: []Status{StatusRunning, StatusAsking, StatusWaiting, StatusNeedsAttention}[rng.Intn(4)], Reason: "x"}
	case 7:
		return EvWorkspaceAcquired, WorkspaceAcquired{Lease: workspace.Lease{Provider: "git", Path: "/tmp/w", Branch: "b", Base: "main", BaseSHA: "abc"}}
	case 8:
		return EvWorkspaceReleased, WorkspaceReleased{Forced: rng.Intn(2) == 0}
	case 9:
		return EvWaitPolled, WaitPolled{Seq: rng.Intn(seq + 1), N: rng.Intn(9), LastLine: "waiting"}
	case 10:
		return EvSlicesProposed, SlicesProposed{Seq: seq, Slices: []Slice{{Key: "a", Title: "A", File: "01-a.md", Number: 1}}}
	case 11:
		return EvSlicesApproved, SeqOnly{Seq: rng.Intn(seq + 1)}
	case 12:
		return EvChildStarted, ChildStarted{ChildID: fmt.Sprintf("p.%02d-x", seq), SliceKey: "x", Number: seq}
	case 13:
		return EvVisitInterrupted, SeqOnly{Seq: rng.Intn(seq + 1)}
	case 14:
		return EvBaseMoved, BaseMoved{OldSHA: "a", NewSHA: "b"}
	default:
		return EvCommand, Command{Name: "retry", Actor: ActorUser, Source: "cli"}
	}
}

func TestReplayMatchesIncremental(t *testing.T) {
	for trial := 0; trial < 50; trial++ {
		rng := rand.New(rand.NewSource(int64(trial)))
		st, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		r, err := st.Create("run")
		if err != nil {
			t.Fatal(err)
		}
		clock := time.Date(2026, 10, 4, 19, 0, 0, 0, time.UTC)
		r.now = func() time.Time { clock = clock.Add(time.Duration(rng.Intn(5000)) * time.Millisecond); return clock }
		if _, err := r.Emit(EvRunCreated, ActorEngine, RunCreated{ID: "run", Pipeline: "p", Start: "implement", Vars: map[string]string{"a": "1"}}); err != nil {
			t.Fatal(err)
		}
		n := rng.Intn(200)
		for i := 0; i < n; i++ {
			typ, data := randomEvent(rng, i+2)
			if _, err := r.Emit(typ, ActorEngine, data); err != nil {
				t.Fatal(err)
			}
		}
		live := r.Snapshot()
		r.Close()
		rebuilt, _, err := Rebuild(r.Dir())
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(live)
		b, _ := json.Marshal(rebuilt)
		if string(a) != string(b) {
			t.Fatalf("trial %d: snapshot mismatch\nlive    %s\nrebuilt %s", trial, a, b)
		}
		onDisk, err := st.Load("run")
		if err != nil {
			t.Fatal(err)
		}
		c, _ := json.Marshal(onDisk)
		if string(a) != string(c) {
			t.Fatalf("trial %d: run.json differs from live snapshot", trial)
		}
	}
}

func TestTruncatedTrailingLine(t *testing.T) {
	st, _ := New(t.TempDir())
	r, _ := st.Create("x")
	r.Emit(EvRunCreated, ActorEngine, RunCreated{ID: "x", Start: "a"})
	r.Emit(EvVarSet, ActorEngine, VarSet{Name: "k", Value: "v"})
	r.Close()
	path := filepath.Join(st.RunDir("x"), EventsFile)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":3,"ts":"2026-10-04T`)
	f.Close()

	r2, err := st.Open("x")
	if err != nil {
		t.Fatal(err)
	}
	if s := r2.Snapshot(); s.LastEventSeq != 2 || s.Vars["k"] != "v" {
		t.Fatalf("%+v", s)
	}
	if _, err := r2.Emit(EvVarSet, ActorEngine, VarSet{Name: "k", Value: "w"}); err != nil {
		t.Fatal(err)
	}
	r2.Close()
	s, evs, err := Rebuild(st.RunDir("x"))
	if err != nil || len(evs) != 3 || s.Vars["k"] != "w" {
		t.Fatalf("%v %d %+v", err, len(evs), s)
	}
}

func TestResolve(t *testing.T) {
	st, _ := New(t.TempDir())
	for _, id := range []string{"20261004-191233-rate-limit-a3f9", "20261004-191233-rate-limit-a3f9.01-schema", "20261005-000000-other-bbbb"} {
		r, _ := st.Create(id)
		r.Emit(EvRunCreated, ActorEngine, RunCreated{ID: id})
		r.Close()
	}
	if id, err := st.Resolve("bbbb"); err != nil || id != "20261005-000000-other-bbbb" {
		t.Error(id, err)
	}
	if id, err := st.Resolve("01-schema"); err != nil || id != "20261004-191233-rate-limit-a3f9.01-schema" {
		t.Error(id, err)
	}
	if id, err := st.Resolve("20261004-191233-rate-limit-a3f9"); err != nil || id != "20261004-191233-rate-limit-a3f9" {
		t.Error("exact match should win", id, err)
	}
	if _, err := st.Resolve("rate"); err == nil {
		t.Error("want ambiguous")
	}
	if _, err := st.Resolve("zzz"); err == nil {
		t.Error("want not found")
	}
}

func TestLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l")
	l, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open file description, so a second open in-process conflicts.
	if _, err := TryLock(p); err != ErrLocked {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	l.Unlock()
	if IsLocked(p) {
		t.Fatal("still locked")
	}
}
