package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UserConfigDir is where Claude Code keeps .claude.json.
func UserConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return home
}

// MCPServers returns the MCP servers the user's Claude Code has for a
// project, by name: user scope (dir/.claude.json's mcpServers), then project
// scope (the project's .mcp.json), then local scope (.claude.json's entry
// for the project), each overriding the last as Claude Code does. dirs are
// the project's directories (the repo and its worktree); each one's
// .mcp.json and local entry count. A missing file has no servers.
func MCPServers(dir string, dirs ...string) map[string]json.RawMessage {
	var user struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"projects"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".claude.json")); err == nil {
		_ = json.Unmarshal(b, &user)
	}
	out := map[string]json.RawMessage{}
	add := func(m map[string]json.RawMessage) {
		for k, v := range m {
			out[k] = v
		}
	}
	add(user.MCPServers)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		var project struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if b, err := os.ReadFile(filepath.Join(d, ".mcp.json")); err == nil {
			_ = json.Unmarshal(b, &project)
		}
		add(project.MCPServers)
	}
	for _, d := range dirs {
		if d != "" {
			add(user.Projects[d].MCPServers)
		}
	}
	return out
}

// MCPNames returns the servers' names, sorted.
func MCPNames(servers map[string]json.RawMessage) []string {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// MCPConfig builds an --mcp-config JSON with the named servers, or says
// which names aren't configured.
func MCPConfig(names []string, servers map[string]json.RawMessage) (string, error) {
	pick := map[string]json.RawMessage{}
	var missing []string
	for _, n := range names {
		s, ok := servers[n]
		if !ok {
			missing = append(missing, n)
			continue
		}
		pick[n] = s
	}
	if len(missing) > 0 {
		have := "none are configured (claude mcp add)"
		if len(servers) > 0 {
			have = "configured: " + strings.Join(MCPNames(servers), ", ")
		}
		return "", fmt.Errorf("no MCP server named %s in your Claude Code config; %s", strings.Join(missing, ", "), have)
	}
	b, err := json.Marshal(map[string]any{"mcpServers": pick})
	return string(b), err
}
