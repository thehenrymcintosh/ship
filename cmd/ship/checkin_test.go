package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thehenrymcintosh/ship/internal/daemon"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// A check-in with the agent's decision shows its headline and
// recommendation in the inbox, and Do recommended answers it from there.
func TestInboxDoRecommended(t *testing.T) {
	h := newHarness(t, map[string]string{"p": askPipeline})
	id := h.startRun("p", `work:
  - outcome: done
    summary: did it
    decision: {headline: "Ship the rate limiter now?", situation: "Tests pass; docs are thin.", recommended: "yes", reason: "Nothing blocks it.",
      options: [{choice: "yes", consequence: "the run finishes"}, {choice: "no", consequence: "the run stops"}]}
`)
	h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusAsking })

	var card daemon.CardView
	if err := h.c.Do("GET", "/api/runs/"+id+"/card", nil, &card); err != nil {
		t.Fatal(err)
	}
	if card.Kind != "decision" || card.Headline != "Ship the rate limiter now?" || card.Question != "Ship it?" || card.Source != "agent" ||
		card.Recommended != "yes" || !card.DoRecommended || card.From != "work" || len(card.Options) != 2 || !card.Options[0].Recommended ||
		card.Options[1].Consequence != "the run stops" {
		t.Fatalf("%+v", card)
	}

	inbox := h.get("/fragments/inbox")
	for _, want := range []string{"ci-decision", "Ship the rate limiter now?", "Nothing blocks it.", "Do recommended: yes", `hx-post="/api/runs/` + id + `/answer" hx-vals='{&#34;choice&#34;:&#34;yes&#34;}'`} {
		if !strings.Contains(inbox, want) {
			t.Fatalf("inbox lacks %s:\n%s", want, inbox)
		}
	}
	if runs := h.get("/fragments/runs?filter=inbox"); !strings.Contains(runs, `<span class="ci-tag ci-decision">Decision</span>`) {
		t.Fatalf("runs list lacks the decision tag:\n%s", runs)
	}

	// The CLI shows the same card, recommendation first.
	out, err := exec.Command(shipBin, "--home", h.home, "--no-color", "status", id).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	o := string(out)
	if i, j := strings.Index(o, "→ recommended: yes: Nothing blocks it."), strings.Index(o, "Tests pass; docs are thin."); !strings.Contains(o, "▌DECISION") || i < 0 || j < i {
		t.Fatalf("status:\n%s", o)
	}

	// What the button posts.
	if err := h.c.Do("POST", "/api/runs/"+id+"/answer", map[string]string{"choice": card.Recommended}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusDone })
}

// A denied tool is diagnosed as a problem with an Allow button, which adds
// it to the pipeline's allowed_tools and runs the step again.
func TestAllowDeniedTool(t *testing.T) {
	h := newHarness(t, map[string]string{"p": "# keep this comment\n" + askPipeline})
	id := h.startRun("p", "work: [{outcome: done, summary: blocked, denials: [WebFetch]}, {outcome: done, summary: fetched}]\n")
	h.waitFor(id, func(s *store.RunSnapshot) bool { return s.Status == store.StatusAsking })

	var card daemon.CardView
	if err := h.c.Do("GET", "/api/runs/"+id+"/card", nil, &card); err != nil {
		t.Fatal(err)
	}
	if card.Kind != "problem" || card.Label != "Something broke" || card.Fix == nil || card.Fix.Kind != "allow_tool" || card.Fix.Value != "WebFetch" ||
		card.FixFile != filepath.Join(".ship", "pipelines", "p.yml") || card.FixWrite != "agent:\n  allowed_tools: [Bash, WebFetch]" || card.FixStep != "work" {
		t.Fatalf("%+v %+v", card, card.Fix)
	}
	if page := h.get("/runs/" + id); !strings.Contains(page, "Allow WebFetch for this pipeline") || !strings.Contains(page, "ci-problem") {
		t.Fatalf("run page lacks the fix:\n%s", page)
	}
	if err := h.c.Do("POST", "/api/runs/"+id+"/fix", map[string]string{"kind": card.Fix.Kind, "step": card.FixStep, "value": card.Fix.Value}, nil); err != nil {
		t.Fatal(err)
	}
	s := h.waitFor(id, func(s *store.RunSnapshot) bool {
		n := 0
		for _, v := range s.Visits {
			if v.Step == "work" && v.Finished != nil {
				n++
			}
		}
		return s.Status == store.StatusAsking && n == 2
	})
	b, _ := os.ReadFile(filepath.Join(h.repo, ".ship", "pipelines", "p.yml"))
	if !strings.Contains(string(b), "# keep this comment") || !strings.Contains(string(b), "- WebFetch") {
		t.Fatalf("pipeline:\n%s", b)
	}
	if s.Upgrades != 1 {
		t.Fatalf("the run should be on the edited pipeline: %+v", s)
	}
}
