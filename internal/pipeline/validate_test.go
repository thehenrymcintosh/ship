package pipeline

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// testAgentCheck mirrors the claude adapter's rules without importing it.
func testAgentCheck(cfg AgentConfig) []string {
	var out []string
	cli := cfg.CLI
	if cli == "" {
		cli = "claude"
	}
	if cli != "claude" && cli != "fake" {
		return []string{fmt.Sprintf("unknown agent cli %q", cli)}
	}
	if cfg.Effort != "" && !contains([]string{"low", "medium", "high", "xhigh", "max"}, cfg.Effort) {
		out = append(out, fmt.Sprintf("invalid effort %q", cfg.Effort))
	}
	return out
}

func validateForTest(t *testing.T, path string) []Finding {
	t.Helper()
	_, findings := ValidateFile(path, Options{AgentCheck: testAgentCheck})
	return findings
}

func TestValidPipelines(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/pipelines/valid/*.yml")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		for _, fd := range validateForTest(t, f) {
			if fd.Severity == SevError {
				t.Errorf("%s", fd)
			} else {
				t.Logf("%s", fd)
			}
		}
	}
}

func TestInvalidPipelinesGolden(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/pipelines/invalid/*.yml")
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".yml")
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			for _, fd := range validateForTest(t, f) {
				fd.File = filepath.Base(fd.File)
				b.WriteString(fd.String() + "\n")
			}
			golden := filepath.Join("../../testdata/golden/validate", name+".txt")
			if *update {
				_ = os.MkdirAll(filepath.Dir(golden), 0o755)
				if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if b.String() != string(want) {
				t.Errorf("findings differ\n--- got\n%s--- want\n%s", b.String(), want)
			}
			// Each fixture is named after the codes it must produce.
			for _, code := range strings.Split(name, "_") {
				if len(code) == 4 && (code[0] == 'e' || code[0] == 'w') {
					if !strings.Contains(b.String(), " "+strings.ToUpper(code)+":") {
						t.Errorf("expected a %s finding", strings.ToUpper(code))
					}
				}
			}
		})
	}
}

func TestDurations(t *testing.T) {
	for in, want := range map[string]string{"90s": "1m30s", "7d": "168h0m0s", "1d12h": "36h0m0s", "5m": "5m0s"} {
		d, err := ParseDuration(in)
		if err != nil || d.String() != want {
			t.Errorf("%s: got %v %v", in, d, err)
		}
	}
	for _, bad := range []string{"", "5", "d", "1w"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestOutcomesAndTargets(t *testing.T) {
	f, fs := ParseFile("../../testdata/pipelines/valid/slice.yml")
	if f == nil {
		t.Fatal(fs)
	}
	p := f.Pipeline
	if got := p.Steps["review"].OutcomeNames(); strings.Join(got, ",") != "pass,changes,stuck" {
		t.Errorf("review outcomes %v", got)
	}
	if got := p.Steps["check-in"].OutcomeNames(); strings.Join(got, ",") != "retry,rework,back to review,abandon" {
		t.Errorf("check-in outcomes %v", got)
	}
	if tgt, _ := p.Steps["implement"].Target("done"); tgt != "review" {
		t.Errorf("implement → %s", tgt)
	}
	if p.OnError(p.Steps["gate"]) != "check-in" || p.MaxVisits(p.Steps["in-review"]) != 20 || p.MaxVisits(p.Steps["gate"]) != 3 {
		t.Error("defaults not applied")
	}
	if !strings.Contains(p.Mermaid(), "n_check_in -.->|retry| n_implement") {
		t.Errorf("mermaid:\n%s", p.Mermaid())
	}
}

func TestSchemaIsCommitted(t *testing.T) {
	committed, err := os.ReadFile("../../schema/pipeline.json")
	if err != nil {
		t.Skip("schema not generated yet")
	}
	if string(committed) != string(SchemaJSON()) {
		t.Error("schema/pipeline.json is stale; run `make schema`")
	}
}

func TestGraphHidesCatchAllCheckIns(t *testing.T) {
	f, fs := Parse("implicit.yml", []byte(`version: 1
start: build
defaults:
  on_error: check-in
steps:
  build:
    prompt: Build it.
    next: {done: test}
  test:
    run: make test
    next: {pass: done, fail: build}
  check-in:
    ask: "Stopped at {{came_from}}. What next?"
    next: {retry: $came_from, abandon: stop}
`))
	if f == nil {
		t.Fatal(fs)
	}
	g := f.Pipeline.Graph()
	for _, n := range g.Nodes {
		if want := n.ID == "check-in" || n.ID == TargetStop; n.Fallback != want {
			t.Errorf("node %s fallback=%v", n.ID, n.Fallback)
		}
	}
	for _, e := range g.Edges {
		if want := e.From == "check-in" || e.To == "check-in" || e.To == TargetStop; e.Fallback != want {
			t.Errorf("edge %s→%s (%s) fallback=%v", e.From, e.To, e.Kind, e.Fallback)
		}
	}
	m := f.Pipeline.Mermaid()
	if !strings.HasPrefix(m, "flowchart LR") || strings.Contains(m, "check_in") || !strings.Contains(m, "n_test") {
		t.Errorf("mermaid:\n%s", m)
	}
}
