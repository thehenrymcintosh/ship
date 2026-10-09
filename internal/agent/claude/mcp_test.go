package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Named MCP servers come from the user's Claude Code config: user scope,
// overridden by the project's .mcp.json, overridden by local scope.
func TestMCPServers(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude.json"), `{"mcpServers":{"linear":{"url":"user"},"github":{"command":"gh"}},
		"projects":{"`+repo+`":{"mcpServers":{"sentry":{"url":"local"}}}}}`)
	write(filepath.Join(repo, ".mcp.json"), `{"mcpServers":{"linear":{"url":"project"}}}`)
	servers := MCPServers(home, repo, "")
	if got := strings.Join(MCPNames(servers), ","); got != "github,linear,sentry" {
		t.Fatalf("names %s", got)
	}
	cfg, err := MCPConfig([]string{"linear", "sentry"}, servers)
	if err != nil || cfg != `{"mcpServers":{"linear":{"url":"project"},"sentry":{"url":"local"}}}` {
		t.Errorf("config %s, %v", cfg, err)
	}
	if _, err := MCPConfig([]string{"jira"}, servers); err == nil || !strings.Contains(err.Error(), "jira") || !strings.Contains(err.Error(), "github, linear, sentry") {
		t.Errorf("unknown name: %v", err)
	}
	if got := MCPServers(t.TempDir()); len(got) != 0 {
		t.Errorf("no config: %v", got)
	}
}
