package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// A folder pipeline's agents load the folder (as the worktree has it) as a
// plugin, its skills version the pipeline, and its history lives in it.
func TestFolderPipelineRun(t *testing.T) {
	en := newEnv(t, map[string]string{"other": "version: 1\nstart: a\nsteps:\n  a: {run: 'true', next: {pass: done, fail: stop}}\n"})
	folder := filepath.Join(en.repo, ".ship", "pipelines", "fp")
	os.MkdirAll(filepath.Join(folder, "skills", "greet"), 0o755)
	writeFile(t, filepath.Join(folder, "pipeline.yml"), "version: 1\nstart: greet\nsteps:\n  greet:\n    agent: /greet\n    next: done\n")
	writeFile(t, filepath.Join(folder, "skills", "greet", "SKILL.md"), "---\nname: greet\n---\nSay hi.\n")
	gitRun(t, en.repo, "add", ".")
	gitRun(t, en.repo, "commit", "-qm", "folder pipeline")

	out := filepath.Join(en.home, "plugin-dirs.txt")
	snap := en.start("fp", "", nil, "greet: [{outcome: done, summary: hi, run: 'echo \"$SHIP_FAKE_PLUGIN_DIRS\" > "+out+"'}]\n")
	s := en.waitStatus(snap.ID, store.StatusDone)

	if s.HistoryDir != folder {
		t.Fatalf("history dir %s, want the folder", s.HistoryDir)
	}
	if s.PipelineFolders["fp"] != ".ship/pipelines/fp" {
		t.Fatalf("folders %v", s.PipelineFolders)
	}
	// The plugin is built in the run's dir from the worktree's skills; the
	// checkout isn't touched.
	b, _ := os.ReadFile(out)
	got := strings.TrimSpace(string(b))
	runDir := en.e.o.Store.RunDir(snap.ID)
	if got != filepath.Join(runDir, pluginDir) {
		t.Fatalf("plugin dir %q, want it in the run dir %s", got, runDir)
	}
	if _, err := os.Stat(filepath.Join(got, "skills", "greet", "SKILL.md")); err != nil {
		t.Fatal("the folder's skills aren't in the plugin")
	}
	if b, _ := os.ReadFile(filepath.Join(got, pipeline.PluginManifest)); !strings.Contains(string(b), `"name": "fp"`) {
		t.Fatalf("manifest: %s", b)
	}
	if st, _ := exec.Command("git", "-C", en.repo, "status", "--porcelain", "--", ".ship/pipelines").Output(); strings.Contains(string(st), "claude-plugin") {
		t.Fatalf("wrote into the checkout:\n%s", st)
	}
	vs, err := history.Open(folder, t.TempDir()).Versions()
	if err != nil || len(vs) != 1 {
		t.Fatal(vs, err)
	}
	var skill *history.Part
	for i, p := range vs[0].Parts {
		if p.Kind == history.KindSkill && p.Name == "greet" {
			skill = &vs[0].Parts[i]
		}
	}
	if skill == nil || skill.Missing {
		t.Fatalf("the folder skill should be part of the version: %+v", vs[0].Parts)
	}
}
