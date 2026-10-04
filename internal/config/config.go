// Package config loads and merges ship configuration: built-in
// defaults < ~/.ship/config.yml < <repo>/.ship/config.yml. Maps merge key by
// key, lists replace.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
	"github.com/invopop/jsonschema"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
)

// Config is the merged configuration.
type Config struct {
	Workspace     Workspace            `yaml:"workspace"`
	Agent         pipeline.AgentConfig `yaml:"agent"`
	MaxAgents     int                  `yaml:"max_agents" jsonschema:"minimum=1"`
	Notifications bool                 `yaml:"notifications"`
	UI            UI                   `yaml:"ui"`
	Retention     Retention            `yaml:"retention"`
}

// Workspace configures workspace providers.
type Workspace struct {
	Provider      string `yaml:"provider" jsonschema:"enum=git,enum=treehouse,enum=none"`
	Fetch         bool   `yaml:"fetch"`
	ReleaseOnDone bool   `yaml:"release_on_done"`
	Git           Git    `yaml:"git"`
}

// Git configures the git worktree provider.
type Git struct {
	Dir   string   `yaml:"dir"`
	Setup []string `yaml:"setup"`
}

// UI configures the web UI.
type UI struct {
	Port        int    `yaml:"port" jsonschema:"minimum=1,maximum=65535"`
	OpenOnStart bool   `yaml:"open_on_start"`
	Editor      string `yaml:"editor"`
	Terminal    string `yaml:"terminal"`
}

// Retention controls `ship clean`.
type Retention struct {
	KeepRunsDays int `yaml:"keep_runs_days" jsonschema:"minimum=0"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		Workspace: Workspace{
			Provider:      "git",
			Fetch:         true,
			ReleaseOnDone: true,
			Git:           Git{Dir: "{repo_parent}/{repo}" + brand.WorktreeSuffix + "/{run}", Setup: []string{}},
		},
		Agent:         pipeline.AgentConfig{CLI: "claude", PermissionMode: "acceptEdits", AllowedTools: []string{}},
		MaxAgents:     3,
		Notifications: true,
		UI:            UI{Port: 7420, OpenOnStart: true},
		Retention:     Retention{KeepRunsDays: 30},
	}
}

// Home returns the user-level directory: $SHIP_HOME or ~/.ship.
func Home() string {
	if h := os.Getenv(brand.HomeEnv); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return brand.Dir
	}
	return filepath.Join(home, brand.Dir)
}

// UserFile is <home>/config.yml.
func UserFile(home string) string { return filepath.Join(home, "config.yml") }

// RepoFile is <repo>/.ship/config.yml.
func RepoFile(repo string) string { return filepath.Join(repo, brand.Dir, "config.yml") }

// Load merges defaults, the user file and (when repo != "") the project file.
func Load(home, repo string) (Config, error) {
	base, err := toMap(Defaults())
	if err != nil {
		return Config{}, err
	}
	files := []string{UserFile(home)}
	if repo != "" {
		files = append(files, RepoFile(repo))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Config{}, err
		}
		var m map[string]any
		if err := yaml.Unmarshal(data, &m); err != nil {
			return Config{}, fmt.Errorf("%s: %s", f, yaml.FormatError(err, false, true))
		}
		base = Merge(base, m)
	}
	out, err := yaml.Marshal(base)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.UnmarshalWithOptions(out, &c, yaml.Strict()); err != nil {
		return Config{}, fmt.Errorf("config: %s", yaml.FormatError(err, false, false))
	}
	if c.MaxAgents < 1 {
		c.MaxAgents = 1
	}
	return c, nil
}

func toMap(v any) (map[string]any, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, yaml.Unmarshal(b, &m)
}

// Merge merges b over a: maps merge key by key, everything else replaces.
func Merge(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, bv := range b {
		am, aok := out[k].(map[string]any)
		bm, bok := bv.(map[string]any)
		if aok && bok {
			out[k] = Merge(am, bm)
			continue
		}
		out[k] = bv
	}
	return out
}

// SchemaJSON returns the config JSON Schema (`ship schema --config`).
func SchemaJSON() []byte {
	r := &jsonschema.Reflector{FieldNameTag: "yaml", RequiredFromJSONSchemaTags: true}
	s := r.Reflect(&Config{})
	s.ID = brand.ConfigSchemaID
	s.Title = brand.Name + " config"
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}
