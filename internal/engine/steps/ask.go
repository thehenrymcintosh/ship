package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/merlin-digital/ship/internal/pipeline"
	"github.com/merlin-digital/ship/internal/store"
	"github.com/merlin-digital/ship/internal/tmpl"
)

// Ask executes `ask` steps.
type Ask struct{}

// Execute posts the question and blocks until a human answers.
func (Ask) Execute(ctx context.Context, v *Visit) (Result, error) {
	if !v.Resumed {
		q, err := tmpl.Render(pipeline.Str(v.Step.Ask), v.Scope, tmpl.Plain)
		if err != nil {
			return renderError(err), nil
		}
		_ = writeFile(v.File("input.md"), q+"\n\nChoices: "+strings.Join(v.Step.Choices.Keys(), " · ")+"\n")
		if err := v.RT.Emit(store.EvAskPending, store.AskPending{
			Seq: v.Seq, Kind: store.AskKindAsk, Question: q, Choices: v.Step.Choices.Keys(),
			Input: v.Step.InputOrDefault(), Show: v.Step.Show,
		}); err != nil {
			return Result{}, err
		}
		if err := v.RT.SetStatus(store.StatusAsking, "ask: "+v.StepName); err != nil {
			return Result{}, err
		}
		v.RT.Notify(v.Snapshot.Title, q)
	} else if err := v.RT.SetStatus(store.StatusAsking, "ask: "+v.StepName); err != nil {
		return Result{}, err
	}

	for {
		cmd, err := v.RT.Await(ctx)
		if err != nil {
			return Result{Outcome: OutcomeCancelled, Summary: "cancelled"}, nil
		}
		if cmd.Name != "answer" {
			cmd.Respond(&InvalidError{"this step is an ask; answer it with a choice"})
			continue
		}
		if err := CheckAnswer(v.Step, cmd.Choice, cmd.Note); err != nil {
			cmd.Respond(err)
			continue
		}
		if err := v.RT.Emit(store.EvAskAnswered, store.AskAnswered{Seq: v.Seq, Choice: cmd.Choice, Note: cmd.Note}); err != nil {
			cmd.Respond(err)
			return Result{}, err
		}
		cmd.Respond(nil)
		summary := strings.TrimSpace(cmd.Note)
		if summary == "" {
			summary = "Chose " + cmd.Choice
		}
		return Result{Outcome: cmd.Choice, Summary: summary, HumanReset: true}, nil
	}
}

// CheckAnswer validates a choice and note against an ask step.
func CheckAnswer(s *pipeline.Step, choice, note string) error {
	if _, ok := s.Choices.Get(choice); !ok {
		return &InvalidError{fmt.Sprintf("%q isn't a choice (choose one of: %s)", choice, strings.Join(s.Choices.Keys(), ", "))}
	}
	if s.InputOrDefault() == "required" && strings.TrimSpace(note) == "" {
		return &InvalidError{"a note is required"}
	}
	return nil
}
