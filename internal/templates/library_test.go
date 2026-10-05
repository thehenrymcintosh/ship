package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/pipeline"
)

// Every built-in template must install cleanly: its pipelines validate, and
// every skill its agent steps call ships with it.
func TestBuiltinTemplatesAreSound(t *testing.T) {
	all, err := List(Sources{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Skip("no built-in templates")
	}
	for _, tpl := range all {
		t.Run(tpl.Name, func(t *testing.T) {
			if tpl.Description == "" {
				t.Error("template.yml needs a description")
			}
			root := t.TempDir()
			ship, skills := filepath.Join(root, ".ship"), filepath.Join(root, ".claude", "skills")
			if _, err := tpl.Install(ship, skills, false); err != nil {
				t.Fatal(err)
			}
			l := pipeline.NewLoader(filepath.Join(ship, "pipelines"))
			for _, name := range tpl.Pipelines() {
				f, findings := l.Validate(name, pipeline.Options{})
				for _, fd := range findings {
					t.Errorf("%s", fd)
				}
				if f == nil {
					continue
				}
				for _, sn := range f.Pipeline.SortedSteps() {
					s := f.Pipeline.Steps[sn]
					for _, line := range []*string{s.Agent, s.Split} {
						l := strings.TrimSpace(pipeline.Str(line))
						if !strings.HasPrefix(l, "/") {
							continue
						}
						skill := strings.TrimPrefix(strings.Fields(l)[0], "/")
						b, err := os.ReadFile(filepath.Join(skills, skill, "SKILL.md"))
						if err != nil {
							t.Errorf("step %s calls /%s, which the template doesn't include", sn, skill)
							continue
						}
						if !strings.Contains(string(b), "name: "+skill+"\n") {
							t.Errorf("skills/%s/SKILL.md should have name: %s", skill, skill)
						}
					}
				}
			}
		})
	}
}
