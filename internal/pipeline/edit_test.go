package pipeline

import (
	"strings"
	"testing"
)

func TestSetYAML(t *testing.T) {
	src := `# The release pipeline.
version: 1
start: impl
steps:
  # Writes the code.
  impl:
    prompt: Implement it.  # keep this
    next: review
  review: {prompt: Review it., next: done}
`
	out, err := SetYAML([]byte(src), []string{"agent", "allowed_tools"}, []string{"Bash", "Bash(go test:*)"})
	if err != nil {
		t.Fatal(err)
	}
	out, err = SetYAML(out, []string{"steps", "review", "timeout"}, "40m")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"# The release pipeline.", "# Writes the code.", "prompt: Implement it. # keep this", "agent:\n  allowed_tools:\n    - Bash\n    - Bash(go test:*)", "timeout: 40m"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Replacing keeps a flow list a flow list.
	out, _ = SetYAML([]byte("agent: {allowed_tools: [Bash]}\n"), []string{"agent", "allowed_tools"}, []string{"Bash", "Edit"})
	if string(out) != "agent: {allowed_tools: [Bash, Edit]}\n" {
		t.Fatalf("%q", out)
	}
	if _, err := SetYAML([]byte("steps: [a]\n"), []string{"steps", "x", "timeout"}, "1m"); err == nil {
		t.Fatal("edited through a list")
	}
}
