package pipeline

import (
	"encoding/json"
	"io/fs"
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
// folder's skills/ (replacing whatever dest held).
func BuildPlugin(dest, folder, name, description string) error {
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	b, err := json.MarshalIndent(Manifest{Name: name, Version: pluginVersion, Description: description}, "", "  ")
	if err != nil {
		return err
	}
	manifest := filepath.Join(dest, filepath.FromSlash(PluginManifest))
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(manifest, append(b, '\n'), 0o644); err != nil {
		return err
	}
	src := filepath.Join(folder, SkillsDir)
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dest, SkillsDir, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, info.Mode().Perm())
	})
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
