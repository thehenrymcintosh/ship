package pipeline

import (
	"encoding/json"

	"github.com/invopop/jsonschema"

	"github.com/thehenrymcintosh/ship/internal/brand"
)

// StepNamePattern is the allowed form of step names.
const StepNamePattern = `^[a-z][a-z0-9_-]*$`

// VarNamePattern is the allowed form of variable names.
const VarNamePattern = `^[A-Za-z][A-Za-z0-9_-]*$`

const targetDescription = "A step name, `done`, `stop` or `$came_from`."

// JSONSchema describes a target or an outcome → target map.
func (Next) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: "Where to go next: one target for every outcome, or a map outcome → target. " + targetDescription,
		OneOf: []*jsonschema.Schema{
			{Type: "string"},
			{Type: "object", AdditionalProperties: &jsonschema.Schema{Type: "string"}},
		},
	}
}

// JSONSchema describes an agent's var list or a run step's var → source map.
func (Save) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: "Agent steps: a list of variables the agent must return. Run steps: a map variable → source (last_line | stdout | file:<path> | json:<dotted.path>).",
		OneOf: []*jsonschema.Schema{
			{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
			{Type: "object", AdditionalProperties: &jsonschema.Schema{
				Type:    "string",
				Pattern: `^(last_line|stdout|file:.+|json:.+)$`,
			}},
		},
	}
}

// JSONSchema describes an ordered string map.
func (OrderedMap) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		AdditionalProperties: &jsonschema.Schema{Type: "string"},
	}
}

// JSONSchema describes a duration string.
func (Duration) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Pattern:     durationRE.String(),
		Description: "Go duration syntax plus d for days: 90s, 5m, 2h, 7d.",
	}
}

// JSONSchemaExtend adds name patterns to steps and variables.
func (Pipeline) JSONSchemaExtend(s *jsonschema.Schema) {
	if p, ok := s.Properties.Get("steps"); ok {
		p.PropertyNames = &jsonschema.Schema{Pattern: StepNamePattern}
		p.Description = "Map of step name → step."
	}
	if p, ok := s.Properties.Get("variables"); ok {
		p.PropertyNames = &jsonschema.Schema{Pattern: VarNamePattern}
	}
	if p, ok := s.Properties.Get("start"); ok {
		p.Description = "Name of the first step."
	}
}

// JSONSchemaExtend documents the step fields that need it.
func (Step) JSONSchemaExtend(s *jsonschema.Schema) {
	docs := map[string]string{
		"agent":   "Agent step: a skill invocation such as `/review mode=code`.",
		"prompt":  "Agent step: free-text prompt (appended after `agent` when both are set).",
		"run":     "Run step: a bash script. Exit 0 → pass, otherwise fail.",
		"ask":     "Ask step: a question for a human. Use `choices` instead of `next`.",
		"wait":    "Wait step: a command polled every `every` until its last line names an outcome.",
		"split":   "Split step: an agent splits the brief into slices.",
		"fanout":  "Fanout step: runs the named pipeline once per slice.",
		"choices": "Ask step: label → target. Labels become the outcome.",
	}
	for k, d := range docs {
		if p, ok := s.Properties.Get(k); ok {
			p.Description = d
		}
	}
	// Exit-code keys are written as YAML integers; keep the outcomes map open.
	if p, ok := s.Properties.Get("outcomes"); ok {
		p.Description = "Run step: map exit code (or `default`) → outcome name."
	}
}

// Schema returns the pipeline JSON Schema, generated from the structs.
func Schema() *jsonschema.Schema {
	r := &jsonschema.Reflector{
		FieldNameTag:               "yaml",
		RequiredFromJSONSchemaTags: true,
		DoNotReference:             false,
	}
	s := r.Reflect(&Pipeline{})
	s.ID = brand.SchemaID
	s.Title = brand.Name + " pipeline"
	return s
}

// SchemaJSON returns the indented schema document.
func SchemaJSON() []byte {
	b, err := json.MarshalIndent(Schema(), "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}
