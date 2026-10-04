// Package templates is the library of pipeline templates `ship add`
// installs: built-in ones embedded in the binary, plus the user's own in
// ~/.ship/templates. See library/README.md for the layout.
package templates

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

//go:embed library
var library embed.FS

// Manifest is a template's template.yml.
type Manifest struct {
	Description string `yaml:"description"`
}

// Template is one installable template.
type Template struct {
	Name        string
	Description string
	Source      string // "built-in" or the directory it was loaded from
	fsys        fs.FS
}

// Sources is where templates come from, in increasing priority.
type Sources struct {
	Builtin fs.FS  // nil = the embedded library
	UserDir string // ~/.ship/templates ("" = none)
}

func (s Sources) builtin() fs.FS {
	if s.Builtin != nil {
		return s.Builtin
	}
	sub, _ := fs.Sub(library, "library")
	return sub
}

func load(fsys fs.FS, source string) (map[string]*Template, error) {
	out := map[string]*Template{}
	entries, err := fs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sub, err := fs.Sub(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		t := &Template{Name: e.Name(), Source: source, fsys: sub}
		if b, err := fs.ReadFile(sub, "template.yml"); err == nil {
			var m Manifest
			if err := yaml.Unmarshal(b, &m); err != nil {
				return nil, fmt.Errorf("template %s: template.yml: %w", e.Name(), err)
			}
			t.Description = m.Description
		}
		out[t.Name] = t
	}
	return out, nil
}

// List returns every template, sorted by name. User templates shadow
// built-in ones of the same name.
func List(src Sources) ([]*Template, error) {
	all, err := load(src.builtin(), "built-in")
	if err != nil {
		return nil, err
	}
	if src.UserDir != "" {
		user, err := load(os.DirFS(src.UserDir), src.UserDir)
		if err != nil {
			return nil, err
		}
		for k, v := range user {
			all[k] = v
		}
	}
	out := make([]*Template, 0, len(all))
	for _, t := range all {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ErrNotFound is returned by Get for an unknown name.
var ErrNotFound = errors.New("no such template")

// Get finds a template by name.
func Get(src Sources, name string) (*Template, error) {
	all, err := List(src)
	if err != nil {
		return nil, err
	}
	for _, t := range all {
		if t.Name == name {
			return t, nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrNotFound, name)
}

// Kind of an installed file.
const (
	KindPipeline = "pipeline"
	KindBin      = "bin"
	KindRule     = "rule"
	KindSkill    = "skill"
)

// File is one file a template installs.
type File struct {
	Kind string
	Rel  string // relative to its destination dir (e.g. "pipelines/pr.yml", "ship-review/SKILL.md")
	Data []byte
	Mode os.FileMode
}

// Files lists what the template installs. Files outside pipelines/, bin/,
// rules/ and skills/ (template.yml, READMEs) aren't installed.
func (t *Template) Files() ([]File, error) {
	var out []File
	err := fs.WalkDir(t.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		top, rest, ok := strings.Cut(p, "/")
		if !ok {
			return nil
		}
		data, err := fs.ReadFile(t.fsys, p)
		if err != nil {
			return err
		}
		f := File{Rel: p, Data: data, Mode: 0o644}
		switch top {
		case "pipelines":
			f.Kind = KindPipeline
		case "bin":
			f.Kind, f.Mode = KindBin, 0o755
		case "rules":
			f.Kind = KindRule
		case "skills":
			f.Kind, f.Rel = KindSkill, rest
		default:
			return nil
		}
		out = append(out, f)
		return nil
	})
	return out, err
}

// Pipelines returns the names of the pipelines the template installs.
func (t *Template) Pipelines() []string {
	files, _ := t.Files()
	var out []string
	for _, f := range files {
		if f.Kind == KindPipeline {
			base := path.Base(f.Rel)
			out = append(out, strings.TrimSuffix(strings.TrimSuffix(base, ".yml"), ".yaml"))
		}
	}
	return out
}

// Result reports one installed file.
type Result struct {
	Path    string
	Written bool // false = kept an existing file
}

// Install copies the template's files: pipelines, bin and rules under
// shipDir (<repo>/.ship or ~/.ship), skills under skillsDir. Existing files
// are kept unless force.
func (t *Template) Install(shipDir, skillsDir string, force bool) ([]Result, error) {
	files, err := t.Files()
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("template %s has no pipelines, bin, rules or skills to install", t.Name)
	}
	var out []Result
	for _, f := range files {
		dest := filepath.Join(shipDir, filepath.FromSlash(f.Rel))
		if f.Kind == KindSkill {
			dest = filepath.Join(skillsDir, filepath.FromSlash(f.Rel))
		}
		if _, err := os.Stat(dest); err == nil && !force {
			out = append(out, Result{Path: dest})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return out, err
		}
		if err := os.WriteFile(dest, f.Data, f.Mode); err != nil {
			return out, err
		}
		if err := os.Chmod(dest, f.Mode); err != nil {
			return out, err
		}
		out = append(out, Result{Path: dest, Written: true})
	}
	return out, nil
}
