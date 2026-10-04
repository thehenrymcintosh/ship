// Package brief parses briefs (markdown with YAML front matter,) and
// writes slice briefs.
package brief

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

// Brief is a parsed brief.
type Brief struct {
	Title      string
	Pipeline   string
	Vars       map[string]string
	Acceptance []string
	Slice      *SliceMeta
	Parent     *ParentMeta
	Body       string
	Raw        []byte
}

// SliceMeta identifies a slice brief.
type SliceMeta struct {
	Key    string `yaml:"key" json:"key"`
	Number int    `yaml:"number" json:"number"`
	Count  int    `yaml:"count" json:"count"`
}

// ParentMeta links a slice brief to its parent run.
type ParentMeta struct {
	Run   string `yaml:"run" json:"run"`
	Brief string `yaml:"brief" json:"brief"`
}

type frontMatter struct {
	Title      string         `yaml:"title"`
	Pipeline   string         `yaml:"pipeline"`
	Vars       map[string]any `yaml:"vars"`
	Acceptance []any          `yaml:"acceptance"`
	Slice      *SliceMeta     `yaml:"slice"`
	Parent     *ParentMeta    `yaml:"parent"`
}

// ErrNoFrontMatter is returned when the brief doesn't start with ---.
var ErrNoFrontMatter = errors.New("brief has no front matter: it must start with a --- line, then YAML with at least a title, then ---")

// Split separates front matter from the body.
func Split(data []byte) (fm, body []byte, err error) {
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, nil, ErrNoFrontMatter
	}
	rest := s[4:]
	end := -1
	if strings.HasPrefix(rest, "---\n") || rest == "---" {
		end = 0
	} else if i := strings.Index(rest, "\n---\n"); i >= 0 {
		end = i + 1
	} else if strings.HasSuffix(rest, "\n---") {
		end = len(rest) - 3
	}
	if end < 0 {
		return nil, nil, errors.New("brief front matter is not closed with a --- line")
	}
	fm = []byte(rest[:end])
	b := rest[end:]
	b = strings.TrimPrefix(b, "---")
	b = strings.TrimPrefix(b, "\n")
	return fm, []byte(b), nil
}

// Parse parses a brief.
func Parse(data []byte) (*Brief, error) {
	fmb, body, err := Split(data)
	if err != nil {
		return nil, err
	}
	var fm frontMatter
	if len(bytes.TrimSpace(fmb)) > 0 {
		if err := yaml.Unmarshal(fmb, &fm); err != nil {
			return nil, fmt.Errorf("brief front matter: %s", yaml.FormatError(err, false, true))
		}
	}
	if strings.TrimSpace(fm.Title) == "" {
		return nil, errors.New("brief front matter needs a title")
	}
	b := &Brief{
		Title:    strings.TrimSpace(fm.Title),
		Pipeline: fm.Pipeline,
		Vars:     map[string]string{},
		Slice:    fm.Slice,
		Parent:   fm.Parent,
		Body:     string(body),
		Raw:      data,
	}
	for k, v := range fm.Vars {
		if v == nil {
			b.Vars[k] = ""
			continue
		}
		b.Vars[k] = fmt.Sprint(v)
	}
	for _, a := range fm.Acceptance {
		b.Acceptance = append(b.Acceptance, strings.TrimSpace(fmt.Sprint(a)))
	}
	return b, nil
}

// AcceptanceMarkdown returns the criteria as a markdown bullet list.
func (b *Brief) AcceptanceMarkdown() string {
	return Bullets(b.Acceptance)
}

// Bullets formats items as a markdown list ("(none)" when empty).
func Bullets(items []string) string {
	if len(items) == 0 {
		return "(none given)"
	}
	var sb strings.Builder
	for i, a := range items {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("- " + a)
	}
	return sb.String()
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slug lowercases s, turns runs of non-alphanumerics into "-" and truncates
// to max characters.
func Slug(s string, max int) string {
	out := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(out) > max {
		out = strings.TrimRight(out[:max], "-")
	}
	if out == "" {
		out = "run"
	}
	return out
}

// SliceSpec is one slice proposed by a split agent.
type SliceSpec struct {
	Key        string   `json:"key" yaml:"key"`
	Title      string   `json:"title" yaml:"title"`
	Brief      string   `json:"brief" yaml:"brief"`
	Acceptance []string `json:"acceptance" yaml:"acceptance"`
}

// RenderSlice writes a slice brief.
func RenderSlice(s SliceSpec, number, count int, parentRun, parentBrief string) []byte {
	type sliceFM struct {
		Title  string            `yaml:"title"`
		Slice  SliceMeta         `yaml:"slice"`
		Parent ParentMeta        `yaml:"parent"`
		Vars   map[string]string `yaml:"vars"`
		Accept []string          `yaml:"acceptance"`
	}
	fm, _ := yaml.MarshalWithOptions(sliceFM{
		Title:  s.Title,
		Slice:  SliceMeta{Key: s.Key, Number: number, Count: count},
		Parent: ParentMeta{Run: parentRun, Brief: parentBrief},
		Vars:   map[string]string{},
		Accept: s.Acceptance,
	}, yaml.IndentSequence(true))
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(fm)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(s.Brief))
	b.WriteString("\n")
	return b.Bytes()
}

// SliceFileName returns "NN-<key>.md".
func SliceFileName(number int, key string) string {
	return fmt.Sprintf("%02d-%s.md", number, key)
}
