package brief

import (
	"strings"
	"testing"
)

const sample = `---
title: Rate limit the public API
pipeline: feature
vars:
  ticket: API-123
  n: 3
acceptance:
  - Requests over the limit get 429 with Retry-After
  - Limits are configurable per API key
extra: kept
---

## Context
body here
`

func TestParse(t *testing.T) {
	b, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if b.Title != "Rate limit the public API" || b.Pipeline != "feature" || b.Vars["ticket"] != "API-123" || b.Vars["n"] != "3" {
		t.Fatalf("%+v", b)
	}
	if len(b.Acceptance) != 2 || !strings.HasPrefix(b.Body, "\n## Context") {
		t.Fatalf("acceptance %v body %q", b.Acceptance, b.Body)
	}
	if !strings.HasPrefix(b.AcceptanceMarkdown(), "- Requests") {
		t.Fatal(b.AcceptanceMarkdown())
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"no front matter", "---\ntitle: x\n", "---\npipeline: x\n---\n", "---\ntitle: [\n---\n"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
	if _, err := Parse([]byte("---\ntitle: only\n---")); err != nil {
		t.Error(err)
	}
}

func TestSliceRoundTrip(t *testing.T) {
	data := RenderSlice(SliceSpec{Key: "schema", Title: "Add tables", Brief: "Do it.", Acceptance: []string{"migration"}}, 1, 3, "r1", "/x/brief.md")
	b, err := Parse(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if b.Slice == nil || b.Slice.Key != "schema" || b.Slice.Count != 3 || b.Parent.Run != "r1" || b.Acceptance[0] != "migration" || strings.TrimSpace(b.Body) != "Do it." {
		t.Fatalf("%+v\n%s", b, data)
	}
}

func TestSlug(t *testing.T) {
	if got := Slug("Rate limit the public API!", 30); got != "rate-limit-the-public-api" {
		t.Error(got)
	}
	if got := Slug("A very long title that goes on and on forever", 30); len(got) > 30 || strings.HasSuffix(got, "-") {
		t.Error(got)
	}
}
