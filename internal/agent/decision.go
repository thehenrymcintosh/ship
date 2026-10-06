package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Decision is what an agent (or the check-in summariser) tells the person
// at a check-in: what they're deciding, the situation, the options and
// which one it recommends. It drives the check-in card.
type Decision struct {
	Headline    string           `json:"headline"`
	Situation   string           `json:"situation"`
	Options     []DecisionOption `json:"options,omitempty"`
	Recommended string           `json:"recommended"`
	Reason      string           `json:"reason"`
}

// DecisionOption is one of the check-in's choices and what picking it does.
type DecisionOption struct {
	Choice      string `json:"choice"`
	Consequence string `json:"consequence"`
}

// Option returns the option for choice, or nil.
func (d *Decision) Option(choice string) *DecisionOption {
	for i := range d.Options {
		if d.Options[i].Choice == choice {
			return &d.Options[i]
		}
	}
	return nil
}

// Validate checks a decision against the check-in's choices: a headline,
// a recommended choice that exists, and options only for real choices.
func (d *Decision) Validate(choices []string) error {
	if strings.TrimSpace(d.Headline) == "" {
		return errors.New("decision.headline is empty")
	}
	if strings.ContainsAny(strings.TrimSpace(d.Headline), "\n") {
		return errors.New("decision.headline must be one line")
	}
	if len(choices) == 0 {
		return nil
	}
	if !slices.Contains(choices, d.Recommended) {
		return fmt.Errorf("decision.recommended %q isn't one of the check-in's choices (%s)", d.Recommended, strings.Join(choices, ", "))
	}
	seen := map[string]bool{}
	for _, o := range d.Options {
		if !slices.Contains(choices, o.Choice) {
			return fmt.Errorf("decision option %q isn't one of the check-in's choices (%s)", o.Choice, strings.Join(choices, ", "))
		}
		if seen[o.Choice] {
			return fmt.Errorf("decision option %q is listed twice", o.Choice)
		}
		seen[o.Choice] = true
	}
	return nil
}

// DecisionSchema is the JSON Schema of a decision whose choices are
// limited to choices (any string when empty).
func DecisionSchema(choices []string) map[string]any {
	choice := map[string]any{"type": "string"}
	if len(choices) > 0 {
		choice["enum"] = choices
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"headline", "situation", "recommended", "reason"},
		"properties": map[string]any{
			"headline":  map[string]any{"type": "string", "description": "One line: what the person is deciding"},
			"situation": map[string]any{"type": "string", "description": "2-3 lines: what happened and what's at stake"},
			"options": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"choice", "consequence"},
					"properties": map[string]any{
						"choice":      choice,
						"consequence": map[string]any{"type": "string", "description": "What happens if the person picks it"},
					},
				},
			},
			"recommended": choice,
			"reason":      map[string]any{"type": "string", "description": "One line: why that choice"},
		},
	}
}
