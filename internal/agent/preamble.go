package agent

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"text/template"

	sjs "github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/thehenrymcintosh/ship/internal/brand"
)

//go:embed preamble.tmpl
var preambleSrc string

var preambleTmpl = template.Must(template.New("preamble").Funcs(template.FuncMap{
	"join": strings.Join,
}).Parse(preambleSrc))

// HandoverLimit caps how much of the previous handover the preamble inlines.
const HandoverLimit = 8000

// PrevInfo describes the previous visit for the preamble.
type PrevInfo struct {
	Step, Outcome, HandoverPath, Handover string
}

// OutcomeInfo is one outcome and a description of where it leads.
type OutcomeInfo struct {
	Name, Target string
}

// Preamble is the data of preamble.tmpl.
type Preamble struct {
	Brand        string
	Pipeline     string
	RunID        string
	Step         string
	VisitNumber  int
	Worktree     string
	Branch       string
	BriefPath    string
	Acceptance   string
	Prev         *PrevInfo
	Context      []string
	Rules        string
	Extra        string
	Outcomes     []OutcomeInfo
	Save         []string
	Slices       bool
	HumanOutcome string
}

// TruncateHandover caps a handover, pointing at the file for the rest.
func TruncateHandover(text, path string) string {
	if len(text) <= HandoverLimit {
		return strings.TrimRight(text, "\n")
	}
	cut := text[:HandoverLimit]
	return cut + fmt.Sprintf("\n… (truncated; read the full handover at %s)", path)
}

// Render renders the preamble.
func (p Preamble) Render() string {
	if p.Brand == "" {
		p.Brand = brand.Name
	}
	var b bytes.Buffer
	if err := preambleTmpl.Execute(&b, p); err != nil {
		panic(err)
	}
	return b.String()
}

// OutcomeSchema builds the structured output schema for an agent step.
func OutcomeSchema(outcomes, save []string) json.RawMessage {
	props := map[string]any{
		"outcome": map[string]any{"type": "string", "enum": outcomes},
		"summary": map[string]any{"type": "string"},
	}
	required := []string{"outcome", "summary"}
	if len(save) > 0 {
		vp := map[string]any{}
		for _, v := range save {
			vp[v] = map[string]any{"type": "string"}
		}
		props["vars"] = map[string]any{
			"type": "object", "additionalProperties": false,
			"required": save, "properties": vp,
		}
		required = append(required, "vars")
	}
	b, _ := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": required, "properties": props,
	})
	return b
}

// SlicePattern is the allowed form of slice keys.
const SlicePattern = `^[a-z0-9][a-z0-9-]{0,24}$`

// SplitSchema builds the split step's output schema.
func SplitSchema(outcomes []string, maxSlices int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"outcome", "summary"},
		"properties": map[string]any{
			"outcome": map[string]any{"type": "string", "enum": outcomes},
			"summary": map[string]any{"type": "string"},
			"slices": map[string]any{
				"type": "array", "maxItems": maxSlices,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"key", "title", "brief", "acceptance"},
					"properties": map[string]any{
						"key":        map[string]any{"type": "string", "pattern": SlicePattern},
						"title":      map[string]any{"type": "string"},
						"brief":      map[string]any{"type": "string", "description": "Self-contained markdown brief for this slice"},
						"acceptance": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1},
					},
				},
			},
		},
	})
	return b
}

var (
	schemaCacheMu sync.Mutex
	schemaCache   = map[string]*sjs.Schema{}
	printer       = message.NewPrinter(language.English)
)

// ValidateOutput checks structured output against schema. The engine does
// this regardless of what the adapter enforced.
func ValidateOutput(schema, out json.RawMessage) error {
	if len(bytes.TrimSpace(out)) == 0 || string(bytes.TrimSpace(out)) == "null" {
		return errors.New("no structured result was returned")
	}
	schemaCacheMu.Lock()
	sch, ok := schemaCache[string(schema)]
	if !ok {
		doc, err := sjs.UnmarshalJSON(bytes.NewReader(schema))
		if err != nil {
			schemaCacheMu.Unlock()
			return err
		}
		c := sjs.NewCompiler()
		if err := c.AddResource("output.json", doc); err != nil {
			schemaCacheMu.Unlock()
			return err
		}
		if sch, err = c.Compile("output.json"); err != nil {
			schemaCacheMu.Unlock()
			return err
		}
		schemaCache[string(schema)] = sch
	}
	schemaCacheMu.Unlock()
	inst, err := sjs.UnmarshalJSON(bytes.NewReader(out))
	if err != nil {
		return fmt.Errorf("structured result is not JSON: %v", err)
	}
	if err := sch.Validate(inst); err != nil {
		var ve *sjs.ValidationError
		if errors.As(err, &ve) {
			return errors.New(flatten(ve))
		}
		return err
	}
	return nil
}

func flatten(ve *sjs.ValidationError) string {
	var msgs []string
	var walk func(e *sjs.ValidationError)
	walk = func(e *sjs.ValidationError) {
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			msgs = append(msgs, fmt.Sprintf("at %s: %s", loc, e.ErrorKind.LocalizedString(printer)))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return strings.Join(msgs, "; ")
}

// Output is the decoded structured result.
type Output struct {
	Outcome string            `json:"outcome"`
	Summary string            `json:"summary"`
	Vars    map[string]string `json:"vars,omitempty"`
	Slices  []SliceOut        `json:"slices,omitempty"`
}

// SliceOut is one slice in a split result.
type SliceOut struct {
	Key        string   `json:"key"`
	Title      string   `json:"title"`
	Brief      string   `json:"brief"`
	Acceptance []string `json:"acceptance"`
}

// CorrectionPrompt asks the agent to report again after invalid output.
func CorrectionPrompt(validationErr string) string {
	return "Your previous response did not include a valid structured result: " + validationErr +
		". Report it now using the structured output tool."
}

// RevisitPrompt is the prompt for a `session: continue` revisit.
func RevisitPrompt(step string, visit int, prev *PrevInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are back at step %q (visit %d).", step, visit)
	if prev != nil {
		fmt.Fprintf(&b, " Since your last turn, %q finished with %q:\n<handover>\n%s\n</handover>\n", prev.Step, prev.Outcome, prev.Handover)
	} else {
		b.WriteString("\n")
	}
	b.WriteString("Continue the work, then report your result again.")
	return b.String()
}

// ResumePrompt is the prompt for resume-session after an interruption.
const ResumePrompt = "You were interrupted. Check the current state of the worktree, continue, then report your result."
