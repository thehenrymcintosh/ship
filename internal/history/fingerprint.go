// Package history versions pipelines and collects feedback on them.
//
// A version is a fingerprint of everything that shapes what a pipeline's
// agents do: the pipeline file (and any it fans out to), the skills and
// slash commands its steps call, and the rules files and helper scripts it
// refers to. Feedback is recorded against the version that produced the
// work, so it can be compared across versions.
package history

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
)

// Part kinds.
const (
	KindPipeline = "pipeline"
	KindSkill    = "skill"
	KindCommand  = "command"
	KindRules    = "rules"
	KindScript   = "script"
)

// Part is one input to a version.
type Part struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"` // where it was found ("" when missing)
	Hash    string `json:"hash,omitempty"`
	Missing bool   `json:"missing,omitempty"` // referenced but not found (e.g. a plugin skill)
}

// Fingerprint identifies a version.
type Fingerprint struct {
	Hash  string `json:"hash"`
	Parts []Part `json:"parts"`
}

// Short is the abbreviated hash shown to people.
func (f Fingerprint) Short() string { return short(f.Hash) }

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// Inputs locate what a pipeline refers to.
type Inputs struct {
	// Pipelines is the pipeline and everything it fans out to (main first).
	Pipelines []*pipeline.File
	Repo      string // main checkout; repo-relative references resolve here
	Home      string // ~/.ship ($SHIP_HOME); "$SHIP_HOME/bin/x" resolves here
	ClaudeDir string // ~/.claude; user-level skills and commands
	// Folders maps a folder pipeline's name to its folder (as the run sees
	// it), whose skills come first. Nil uses each file's own folder.
	Folders map[string]string
}

// folder is the pipeline folder f's skills resolve from, or "".
func (in Inputs) folder(f *pipeline.File) string {
	if in.Folders != nil {
		return in.Folders[f.Name]
	}
	return pipeline.FolderOf(f.Path)
}

var (
	repoBinRE = regexp.MustCompile(regexp.QuoteMeta(brand.Dir) + `/bin/([A-Za-z0-9._-]+)`)
	homeBinRE = regexp.MustCompile(`\$\{?` + brand.EnvPrefix + `HOME\}?/bin/([A-Za-z0-9._-]+)`)
)

// Compute fingerprints a pipeline and what it refers to.
func Compute(in Inputs) Fingerprint {
	var parts []Part
	seen := map[string]bool{}
	add := func(p Part) {
		key := p.Kind + "\x00" + p.Name
		if !seen[key] {
			seen[key] = true
			parts = append(parts, p)
		}
	}
	for _, f := range in.Pipelines {
		add(Part{Kind: KindPipeline, Name: f.Name, Path: f.Path, Hash: hashBytes(f.Source)})
		p := f.Pipeline
		for _, sn := range p.SortedSteps() {
			s := p.Steps[sn]
			if s == nil {
				continue
			}
			for _, line := range []*string{s.Agent, s.Split} {
				if name := pipeline.SkillName(pipeline.Str(line)); name != "" {
					add(in.ResolveSkill(name, f.Name, in.folder(f)))
				}
			}
			if s.Rules != "" {
				add(in.file(KindRules, s.Rules))
			}
			for _, c := range s.Context {
				add(in.file(KindRules, c))
			}
			for _, script := range []*string{s.Run, s.Wait} {
				for _, m := range repoBinRE.FindAllStringSubmatch(pipeline.Str(script), -1) {
					add(in.file(KindScript, brand.Dir+"/bin/"+m[1]))
				}
				for _, m := range homeBinRE.FindAllStringSubmatch(pipeline.Str(script), -1) {
					add(in.homeFile(KindScript, "bin/"+m[1]))
				}
			}
		}
	}
	sort.SliceStable(parts, func(i, j int) bool {
		if parts[i].Kind != parts[j].Kind {
			return parts[i].Kind < parts[j].Kind
		}
		return parts[i].Name < parts[j].Name
	})
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%v\n", p.Kind, p.Name, p.Hash, p.Missing)
	}
	return Fingerprint{Hash: hex.EncodeToString(h.Sum(nil)), Parts: parts}
}

// ResolveSkill finds a skill (a directory with SKILL.md) or a slash command
// (a .md file): in the pipeline's folder first (folder may be ""), then the
// repo, then for the user, as Claude Code does. "<pipeline>:skill" names
// the folder's skill explicitly; another plugin's skill is missing (ship
// can't see plugins).
func (in Inputs) ResolveSkill(name, pipe, folder string) Part {
	if plugin, skill, ok := strings.Cut(name, ":"); ok {
		if plugin == pipe {
			if dir := pipeline.FolderSkill(folder, skill); dir != "" {
				return Part{Kind: KindSkill, Name: skill, Path: dir, Hash: hashDir(dir)}
			}
		}
		return Part{Kind: KindSkill, Name: name, Missing: true}
	}
	if dir := pipeline.FolderSkill(folder, name); dir != "" {
		return Part{Kind: KindSkill, Name: name, Path: dir, Hash: hashDir(dir)}
	}
	var roots []string
	if in.Repo != "" {
		roots = append(roots, filepath.Join(in.Repo, ".claude"))
	}
	if in.ClaudeDir != "" {
		roots = append(roots, in.ClaudeDir)
	}
	for _, root := range roots {
		dir := filepath.Join(root, "skills", name)
		if st, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !st.IsDir() {
			return Part{Kind: KindSkill, Name: name, Path: dir, Hash: hashDir(dir)}
		}
		cmd := filepath.Join(root, "commands", name+".md")
		if b, err := os.ReadFile(cmd); err == nil {
			return Part{Kind: KindCommand, Name: name, Path: cmd, Hash: hashBytes(b)}
		}
	}
	return Part{Kind: KindSkill, Name: name, Missing: true}
}

func (in Inputs) file(kind, rel string) Part {
	path := rel
	if !filepath.IsAbs(rel) && in.Repo != "" {
		path = filepath.Join(in.Repo, rel)
	}
	return readPart(kind, rel, path)
}

func (in Inputs) homeFile(kind, rel string) Part {
	if in.Home == "" {
		return Part{Kind: kind, Name: "$" + brand.HomeEnv + "/" + rel, Missing: true}
	}
	return readPart(kind, "$"+brand.HomeEnv+"/"+rel, filepath.Join(in.Home, rel))
}

func readPart(kind, name, path string) Part {
	b, err := os.ReadFile(path)
	if err != nil {
		return Part{Kind: kind, Name: name, Missing: true}
	}
	return Part{Kind: kind, Name: name, Path: path, Hash: hashBytes(b)}
}

func hashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// hashDir hashes every file in a directory (paths and contents).
func hashDir(dir string) string {
	h := sha256.New()
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(dir, f)
		fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(rel), hashBytes(b))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Changed lists parts that differ between two fingerprints, as
// "pipeline pr changed", "skill ship-review added", ….
func Changed(prev, next []Part) []string {
	key := func(p Part) string { return p.Kind + " " + p.Name }
	old := map[string]Part{}
	for _, p := range prev {
		old[key(p)] = p
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range next {
		k := key(p)
		seen[k] = true
		o, ok := old[k]
		switch {
		case !ok:
			out = append(out, k+" added")
		case o.Hash != p.Hash || o.Missing != p.Missing:
			out = append(out, k+" changed")
		}
	}
	for _, p := range prev {
		if !seen[key(p)] {
			out = append(out, key(p)+" removed")
		}
	}
	return out
}
