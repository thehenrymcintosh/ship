package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecisionValidate(t *testing.T) {
	choices := []string{"again", "abandon"}
	ok := Decision{Headline: "Fix R1?", Recommended: "again", Options: []DecisionOption{{Choice: "again"}, {Choice: "abandon"}}}
	if err := ok.Validate(choices); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		d    Decision
		want string
	}{
		"no headline":      {Decision{Recommended: "again"}, "headline is empty"},
		"two lines":        {Decision{Headline: "a\nb", Recommended: "again"}, "one line"},
		"unknown pick":     {Decision{Headline: "h", Recommended: "maybe"}, `"maybe" isn't one of`},
		"unknown option":   {Decision{Headline: "h", Recommended: "again", Options: []DecisionOption{{Choice: "later"}}}, `option "later"`},
		"duplicate option": {Decision{Headline: "h", Recommended: "again", Options: []DecisionOption{{Choice: "again"}, {Choice: "again"}}}, "listed twice"},
	} {
		if err := tc.d.Validate(choices); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOutcomeSchemaDecisionIsOptional(t *testing.T) {
	schema := OutcomeSchemaWithDecision([]string{"pass", "stuck"}, nil, []string{"again", "abandon"})
	if err := ValidateOutput(schema, json.RawMessage(`{"outcome":"pass","summary":"ok"}`)); err != nil {
		t.Fatal(err)
	}
	good := `{"outcome":"stuck","summary":"s","decision":{"headline":"h","situation":"s","recommended":"again","reason":"r","options":[{"choice":"abandon","consequence":"stops"}]}}`
	if err := ValidateOutput(schema, json.RawMessage(good)); err != nil {
		t.Fatal(err)
	}
	bad := `{"outcome":"stuck","summary":"s","decision":{"headline":"h","situation":"s","recommended":"later","reason":"r"}}`
	if err := ValidateOutput(schema, json.RawMessage(bad)); err == nil {
		t.Fatal("accepted a recommendation that isn't a choice")
	}
	if strings.Contains(string(OutcomeSchema([]string{"pass"}, nil)), "decision") {
		t.Fatal("decision offered without a check-in")
	}
}

func TestPreambleAsksForDecision(t *testing.T) {
	p := Preamble{Outcomes: []OutcomeInfo{{Name: "pass", Target: "done"}, {Name: "stuck", Target: "a human check-in", Choices: []string{"again", "abandon"}}}, HumanOutcome: "stuck"}
	out := p.Render()
	if !strings.Contains(out, `decision: include it when the outcome is "stuck", which asks a person to choose: again, abandon`) {
		t.Fatal(out)
	}
	p.Outcomes = p.Outcomes[:1]
	if strings.Contains(p.Render(), "decision:") {
		t.Fatal("asked for a decision without a check-in")
	}
}
