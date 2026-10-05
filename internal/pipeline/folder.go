package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// A pipeline folder (<dir>/<name>/pipeline.yml) is also a Claude Code
// plugin: its skills/<skill>/SKILL.md are the pipeline's own skills, and
// ship passes the folder to every agent of the pipeline with --plugin-dir,
// so `/skill` and `/<name>:skill` both reach them.
const (
	SkillsDir      = "skills"
	PluginManifest = ".claude-plugin/plugin.json"
)

// pluginVersion is the manifest's version. ship's own versions (see the
// history package) are the real record; this stays fixed so the manifest
// doesn't change with every edit.
const pluginVersion = "1.0.0"

// Manifest is the plugin.json ship maintains.
type Manifest struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

// EnsurePlugin writes the folder's plugin manifest when it's missing or
// names another plugin, keeping any other fields. It reports whether it
// wrote.
func EnsurePlugin(folder, name, description string) (bool, error) {
	path := filepath.Join(folder, filepath.FromSlash(PluginManifest))
	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &raw) == nil && raw["name"] == name {
			return false, nil
		}
	}
	raw["name"] = name
	if _, ok := raw["version"]; !ok {
		raw["version"] = pluginVersion
	}
	if _, ok := raw["description"]; !ok && description != "" {
		raw["description"] = description
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, append(b, '\n'), 0o644)
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
