package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/store"
)

func TestCheckInNoteScope(t *testing.T) {
	en := newEnv(t, map[string]string{"p": `version: 1
start: a
defaults: {on_error: stop}
steps:
  a: {prompt: A., next: first}
  first: {ask: "Carry on?", choices: {go: b}}
  b: {prompt: B., next: second}
  second: {ask: "Carry on?", choices: {go: c}}
  c: {prompt: C., next: d}
  d: {prompt: D., next: done}
`})
	s := en.start("p", "", nil, `a: [{outcome: done, summary: did a}]
b: [{outcome: done, summary: did b}]
c: [{outcome: done, summary: did c}]
d: [{outcome: done, summary: did d}]
`)
	asked := func(step string) {
		en.waitFor(s.ID, "ask "+step, func(s *store.RunSnapshot) bool {
			return s.Status == store.StatusAsking && s.CurrentStep == step
		})
	}
	asked("first")
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "go", Note: "Use tabs, never spaces.", For: "sideways"}); KindOf(err) != KindInvalid {
		t.Fatalf("an unknown note scope: %v", err)
	}
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "go", Note: "Use tabs, never spaces.", For: store.NoteForRun}); err != nil {
		t.Fatal(err)
	}
	asked("second")
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "go", Note: "Rename the flag.", For: store.NoteForStep}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	if got := visitTrail(s); got != "a:done first:go b:done second:go c:done d:done" {
		t.Fatalf("trail %q", got)
	}
	if len(s.RunNotes) != 1 || s.RunNotes[0].Step != "first" || s.RunNotes[0].Note != "Use tabs, never spaces." {
		t.Fatalf("run notes %+v", s.RunNotes)
	}
	input := func(step string) string {
		for _, v := range s.Visits {
			if v.Step == step {
				b, _ := os.ReadFile(filepath.Join(en.st.RunDir(s.ID), "visits", v.Dir, "input.md"))
				return string(b)
			}
		}
		t.Fatalf("no visit of %s", step)
		return ""
	}
	runNote := "<run-note from=\"first\">\nUse tabs, never spaces.\n</run-note>"
	for _, step := range []string{"b", "c", "d"} {
		if in := input(step); !strings.Contains(in, runNote) {
			t.Errorf("%s didn't get the run note:\n%s", step, in)
		}
	}
	if in := input("a"); strings.Contains(in, "Use tabs") {
		t.Errorf("a ran before the note was written:\n%s", in)
	}
	// The step note reaches the next step only, as the check-in's handover.
	if in := input("c"); !strings.Contains(in, "Rename the flag.") {
		t.Errorf("c didn't get the step note:\n%s", in)
	}
	if in := input("d"); strings.Contains(in, "Rename the flag.") {
		t.Errorf("d got the step note:\n%s", in)
	}
	// Rebuilding from the log gives the same notes.
	re, _, err := store.Rebuild(en.st.RunDir(s.ID))
	if err != nil || len(re.RunNotes) != 1 {
		t.Fatalf("rebuilt: %v %+v", err, re.RunNotes)
	}
}

func TestRunNotesReachSlices(t *testing.T) {
	en := newEnv(t, map[string]string{"parent": `version: 1
start: plan
steps:
  plan: {ask: "Split it?", choices: {go: split}}
  split:
    split: ""
    next: {ok: build}
  build:
    fanout: child
    mode: series
    next: {done: done, failed: stop}
`, "child": `version: 1
start: work
steps:
  work: {prompt: Build the slice., next: done}
`})
	s := en.start("parent", "", nil, `split:
  - outcome: ok
    summary: one slice
    slices:
      - {key: a, title: A, brief: a, acceptance: [x]}
work: [{outcome: done, summary: built}]
`)
	en.waitStatus(s.ID, store.StatusAsking)
	if err := en.e.Do(s.ID, Command{Name: CmdAnswer, Choice: "go", Note: "Keep the old API working.", For: store.NoteForRun}); err != nil {
		t.Fatal(err)
	}
	s = en.waitStatus(s.ID, store.StatusDone)
	in, _ := os.ReadFile(filepath.Join(en.st.RunDir(s.ID), "visits", s.Visits[1].Dir, "input.md"))
	if !strings.Contains(string(in), "Keep the old API working.") {
		t.Errorf("the split step didn't get the run note:\n%s", in)
	}
	cs, err := en.e.Snapshot(s.Children[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	in, _ = os.ReadFile(filepath.Join(en.st.RunDir(cs.ID), "visits", cs.Visits[0].Dir, "input.md"))
	if !strings.Contains(string(in), `<run-note from="plan" run="`+s.ID+`">`) || !strings.Contains(string(in), "Keep the old API working.") {
		t.Errorf("the slice didn't get its parent's run note:\n%s", in)
	}
}
