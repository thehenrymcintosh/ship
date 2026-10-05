package daemon

import (
	"strings"
	"testing"
)

const reviewHandover = "## Summary\nRisk: medium. The R1–R6 fixes are correct.\n\n" +
	"R1 [warning, auto-fix] internal/engine/steps/pr.go:258-275: a cancelled check is re-run while its run is in progress.\n" +
	"- GitHub refuses to re-run a run that is still in progress.\n" +
	"- Remedy: wait for the run.\n\n" +
	"**R4** [warning, ask-user] pr.go:258 with ghpr.go:340 (`gh run rerun --failed`): re-running also re-runs real failures.\n" +
	"- Matrix jobs are fail-fast.\n\n" +
	"  - nested detail\n\n" +
	"- R5 [critical, auto-fix]: no location here\n\n" +
	"Info, no action needed:\n- runner.go:671: the reply times out.\n\n" +
	"```\nR9 [warning, ask-user] inside a code block\n```\n"

func TestSplitFindings(t *testing.T) {
	parts := splitFindings(reviewHandover)
	var got []string
	for _, p := range parts {
		if f := p.Finding; f != nil {
			got = append(got, strings.Join([]string{f.ID, f.Severity, f.Action, f.Level, string(f.Where), string(f.Title)}, "|"))
		}
	}
	want := []string{
		"R1|warning|auto-fix|warn|internal/engine/steps/pr.go:258-275|a cancelled check is re-run while its run is in progress.",
		"R4|warning|ask-user|warn|pr.go:258 with ghpr.go:340 (<code>gh run rerun --failed</code>)|re-running also re-runs real failures.",
		"R5|critical|auto-fix|danger||no location here",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	r4 := parts[2].Finding
	if !r4.NeedsYou || !strings.Contains(string(r4.Body), "fail-fast") || !strings.Contains(string(r4.Body), "nested detail") {
		t.Fatalf("R4 %+v", r4)
	}
	last := string(parts[len(parts)-1].HTML)
	if !strings.Contains(last, "Info, no action needed") || !strings.Contains(last, "R9 [warning") {
		t.Fatalf("tail %s", last)
	}
	if splitFindings("## Summary\nAll good.\n") != nil {
		t.Fatal("a handover without findings should render as plain markdown")
	}
}
