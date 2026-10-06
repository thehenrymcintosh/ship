package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// A pipeline folder (<dir>/<name>/pipeline.yml) holds the pipeline's own
// skills in skills/<skill>/SKILL.md. ship gives them to every agent of the
// pipeline as a Claude Code plugin named after the pipeline (--plugin-dir),
// so `/skill` and `/<name>:skill` both reach them. The plugin is built in
// the run's dir (see BuildPlugin), so the folder needs no manifest and the
// checkout is never written to.
const (
	SkillsDir      = "skills"
	PluginManifest = ".claude-plugin/plugin.json"
)

// pluginVersion is the manifest's version. ship's own versions (see the
// history package) are the real record.
const pluginVersion = "1.0.0"

// Manifest is the plugin.json ship writes.
type Manifest struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

// BuildPlugin makes dest a Claude Code plugin of a pipeline folder's
// skills: a manifest naming it after the pipeline, and a copy of the
// folder's skills/ (replacing whatever dest held). Symlinks are followed,
// as Claude Code would follow them in the folder itself. An entry that
// can't be read is left out and listed in skipped, so one bad entry doesn't
// cost the run every skill; err is only for a plugin that couldn't be made.
func BuildPlugin(dest, folder, name, description string) (skipped []string, err error) {
	if err := os.RemoveAll(dest); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(Manifest{Name: name, Version: pluginVersion, Description: description}, "", "  ")
	if err != nil {
		return nil, err
	}
	manifest := filepath.Join(dest, filepath.FromSlash(PluginManifest))
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(manifest, append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	src := filepath.Join(folder, SkillsDir)
	if _, err := os.Stat(src); err != nil {
		return nil, nil
	}
	c := &treeCopy{root: src, open: map[string]bool{}}
	err = c.dir(src, filepath.Join(dest, SkillsDir))
	return c.skipped, err
}

// treeCopy copies a tree, following symlinks. open holds the real paths of
// the directories being copied, so a link back up the tree is skipped
// rather than followed forever.
type treeCopy struct {
	root    string
	open    map[string]bool
	skipped []string
}

func (c *treeCopy) skip(p string) {
	rel, _ := filepath.Rel(c.root, p)
	c.skipped = append(c.skipped, filepath.ToSlash(rel))
}

func (c *treeCopy) dir(from, to string) error {
	real, err := filepath.EvalSymlinks(from)
	if err != nil || c.open[real] {
		c.skip(from)
		return nil
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		c.skip(from)
		return nil
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	c.open[real] = true
	defer delete(c.open, real)
	for _, e := range entries {
		p, dst := filepath.Join(from, e.Name()), filepath.Join(to, e.Name())
		info, err := os.Stat(p) // through a symlink, to what it points at
		switch {
		case err != nil || !(info.IsDir() || info.Mode().IsRegular()):
			c.skip(p)
		case info.IsDir():
			if err := c.dir(p, dst); err != nil {
				return err
			}
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				c.skip(p)
				continue
			}
			if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
				return err
			}
		}
	}
	return nil
}

// FolderSkill returns the folder skill's directory if the folder has it
// (skills/<skill>/SKILL.md), else "".
func FolderSkill(folder, skill string) string {
	if folder == "" || skill == "" {
		return ""
	}
	dir := filepath.Join(folder, SkillsDir, skill)
	if st, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !st.IsDir() {
		return dir
	}
	return ""
}
