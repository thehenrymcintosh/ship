package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	sjs "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Pos is a 1-based line and column.
type Pos struct{ Line, Col int }

// File is a parsed pipeline file with source positions.
type File struct {
	Path     string
	Name     string // file basename without extension; how pipelines are referenced
	Source   []byte
	Pipeline *Pipeline
	pos      map[string]Pos // JSON pointer → position of the key (or value for sequence items)
}

// Pos returns the position for a JSON pointer such as "/steps/review/next",
// falling back to the closest ancestor that has one.
func (f *File) Pos(ptr string) Pos {
	for {
		if p, ok := f.pos[ptr]; ok {
			return p
		}
		if ptr == "" {
			return Pos{1, 1}
		}
		i := strings.LastIndex(ptr, "/")
		if i < 0 {
			return Pos{1, 1}
		}
		ptr = ptr[:i]
	}
}

// Ptr builds a JSON pointer from segments.
func Ptr(seg ...string) string {
	var b strings.Builder
	for _, s := range seg {
		b.WriteByte('/')
		s = strings.ReplaceAll(s, "~", "~0")
		b.WriteString(strings.ReplaceAll(s, "/", "~1"))
	}
	return b.String()
}

// NameFromPath returns the pipeline name for a file path: the file's
// basename, or the folder's name for <name>/pipeline.yml.
func NameFromPath(path string) string {
	if f := FolderOf(path); f != "" {
		return filepath.Base(f)
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(strings.TrimSuffix(base, ".yml"), ".yaml")
}

// ParseFile reads and parses a pipeline file.
func ParseFile(path string) (*File, []Finding) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []Finding{{File: path, Line: 1, Col: 1, Severity: SevError, Code: "E001", Message: err.Error()}}
	}
	return Parse(path, data)
}

// Parse parses pipeline source. It returns the file (nil when the source
// can't be decoded) and any E001/E002 findings.
func Parse(path string, data []byte) (*File, []Finding) {
	f := &File{Path: path, Name: NameFromPath(path), Source: data, pos: map[string]Pos{}}
	errAt := func(code string, p Pos, msg string) []Finding {
		return []Finding{{File: path, Line: p.Line, Col: p.Col, Severity: SevError, Code: code, Message: msg}}
	}

	astFile, err := parser.ParseBytes(data, 0)
	if err != nil {
		return nil, errAt("E001", yamlErrPos(err), yamlErrMsg(err))
	}
	if len(astFile.Docs) == 0 || astFile.Docs[0].Body == nil {
		return nil, errAt("E001", Pos{1, 1}, "empty pipeline file")
	}
	if len(astFile.Docs) > 1 {
		return nil, errAt("E001", Pos{1, 1}, "a pipeline file must contain a single YAML document")
	}
	f.pos[""] = Pos{1, 1}
	walk(astFile.Docs[0].Body, "", f.pos)

	var raw any
	if err := yaml.UnmarshalWithOptions(data, &raw); err != nil {
		return nil, errAt("E001", yamlErrPos(err), yamlErrMsg(err))
	}
	doc, err := toJSONValue(raw)
	if err != nil {
		return nil, errAt("E001", Pos{1, 1}, err.Error())
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, errAt("E002", Pos{1, 1}, "a pipeline must be a mapping")
	}

	if findings := f.schemaFindings(doc); len(findings) > 0 {
		return nil, findings
	}

	var p Pipeline
	if err := yaml.UnmarshalWithOptions(data, &p); err != nil {
		return nil, errAt("E002", yamlErrPos(err), yamlErrMsg(err))
	}
	var order struct {
		Steps     yaml.MapSlice `yaml:"steps"`
		Variables yaml.MapSlice `yaml:"variables"`
	}
	_ = yaml.Unmarshal(data, &order)
	for _, it := range order.Steps {
		p.StepOrder = append(p.StepOrder, fmt.Sprint(it.Key))
	}
	for _, it := range order.Variables {
		p.VarOrder = append(p.VarOrder, fmt.Sprint(it.Key))
	}
	if p.Name == "" {
		p.Name = f.Name
	}
	f.Pipeline = &p
	return f, nil
}

// walk records the position of every mapping key and sequence item.
func walk(n ast.Node, ptr string, pos map[string]Pos) {
	switch n := n.(type) {
	case *ast.DocumentNode:
		walk(n.Body, ptr, pos)
	case *ast.TagNode:
		walk(n.Value, ptr, pos)
	case *ast.AnchorNode:
		walk(n.Value, ptr, pos)
	case *ast.MappingNode:
		for _, mv := range n.Values {
			walk(mv, ptr, pos)
		}
	case *ast.MappingValueNode:
		key := keyString(n.Key)
		child := ptr + Ptr(key)
		if tok := n.Key.GetToken(); tok != nil {
			pos[child] = Pos{tok.Position.Line, tok.Position.Column}
		}
		walk(n.Value, child, pos)
	case *ast.SequenceNode:
		for i, v := range n.Values {
			child := fmt.Sprintf("%s/%d", ptr, i)
			if v != nil {
				if tok := v.GetToken(); tok != nil {
					pos[child] = Pos{tok.Position.Line, tok.Position.Column}
				}
			}
			walk(v, child, pos)
		}
	}
}

func keyString(k ast.MapKeyNode) string {
	if s, ok := k.(ast.ScalarNode); ok {
		return fmt.Sprint(s.GetValue())
	}
	if t := k.GetToken(); t != nil {
		return t.Value
	}
	return ""
}

func yamlErrPos(err error) Pos {
	var ye yaml.Error
	if errors.As(err, &ye) {
		if tok := ye.GetToken(); tok != nil {
			return Pos{tok.Position.Line, tok.Position.Column}
		}
	}
	return Pos{1, 1}
}

func yamlErrMsg(err error) string {
	var ye yaml.Error
	if errors.As(err, &ye) {
		return ye.GetMessage()
	}
	return err.Error()
}

// toJSONValue converts decoded YAML into JSON-compatible values (string keys,
// json.Number numbers) for schema validation.
func toJSONValue(v any) (any, error) {
	b, err := json.Marshal(normalize(v))
	if err != nil {
		return nil, err
	}
	return sjs.UnmarshalJSON(bytes.NewReader(b))
}

func normalize(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = normalize(x)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[fmt.Sprint(k)] = normalize(x)
		}
		return out
	case yaml.MapSlice:
		out := make(map[string]any, len(v))
		for _, it := range v {
			out[fmt.Sprint(it.Key)] = normalize(it.Value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = normalize(x)
		}
		return out
	default:
		return v
	}
}

var (
	compiledOnce   sync.Once
	compiledSchema *sjs.Schema
	compileErr     error
)

func compiled() (*sjs.Schema, error) {
	compiledOnce.Do(func() {
		doc, err := sjs.UnmarshalJSON(bytes.NewReader(SchemaJSON()))
		if err != nil {
			compileErr = err
			return
		}
		c := sjs.NewCompiler()
		if err := c.AddResource("pipeline.json", doc); err != nil {
			compileErr = err
			return
		}
		compiledSchema, compileErr = c.Compile("pipeline.json")
	})
	return compiledSchema, compileErr
}

var printer = message.NewPrinter(language.English)

func (f *File) schemaFindings(doc any) []Finding {
	sch, err := compiled()
	if err != nil {
		return []Finding{{File: f.Path, Line: 1, Col: 1, Severity: SevError, Code: "E002", Message: "internal schema error: " + err.Error()}}
	}
	err = sch.Validate(doc)
	if err == nil {
		return nil
	}
	var ve *sjs.ValidationError
	if !errors.As(err, &ve) {
		return []Finding{{File: f.Path, Line: 1, Col: 1, Severity: SevError, Code: "E002", Message: err.Error()}}
	}
	var out []Finding
	seen := map[string]bool{}
	add := func(ptr, msg string) {
		key := ptr + "\x00" + msg
		if seen[key] {
			return
		}
		seen[key] = true
		p := f.Pos(ptr)
		out = append(out, Finding{File: f.Path, Line: p.Line, Col: p.Col, Severity: SevError, Code: "E002", Message: msg})
	}
	var visit func(e *sjs.ValidationError)
	visit = func(e *sjs.ValidationError) {
		ptr := Ptr(e.InstanceLocation...)
		where := dotted(e.InstanceLocation)
		switch k := e.ErrorKind.(type) {
		case *kind.AdditionalProperties:
			for _, prop := range k.Properties {
				add(ptr+Ptr(prop), fmt.Sprintf("unknown field %q in %s", prop, orRoot(where)))
			}
			return
		case *kind.Required:
			add(ptr, fmt.Sprintf("%s: missing required field %s", orRoot(where), quoteJoin(k.Missing)))
			return
		case *kind.OneOf, *kind.AnyOf:
			add(ptr, fmt.Sprintf("%s: %s", orRoot(where), oneOfMessage(e)))
			return
		case *kind.PropertyNames:
			// Descend: the cause names the offending key.
		}
		if len(e.Causes) == 0 {
			add(ptr, fmt.Sprintf("%s: %s", orRoot(where), e.ErrorKind.LocalizedString(printer)))
			return
		}
		for _, c := range e.Causes {
			visit(c)
		}
	}
	visit(ve)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Col < out[j].Col
	})
	return out
}

func oneOfMessage(e *sjs.ValidationError) string {
	// Summarise the expected types of the alternatives.
	var want []string
	for _, c := range e.Causes {
		if t, ok := c.ErrorKind.(*kind.Type); ok {
			want = append(want, t.Want...)
			continue
		}
		// The alternative matched the type but failed deeper; report that.
		inner := c
		for len(inner.Causes) > 0 {
			inner = inner.Causes[0]
		}
		return inner.ErrorKind.LocalizedString(printer)
	}
	if len(want) == 0 {
		return e.ErrorKind.LocalizedString(printer)
	}
	return "expected " + strings.Join(want, " or ")
}

func dotted(loc []string) string { return strings.Join(loc, ".") }

func orRoot(s string) string {
	if s == "" {
		return "pipeline"
	}
	return s
}

func quoteJoin(s []string) string {
	q := make([]string, len(s))
	for i, x := range s {
		q[i] = fmt.Sprintf("%q", x)
	}
	return strings.Join(q, ", ")
}
