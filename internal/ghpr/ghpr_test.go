package ghpr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIListReadsEveryPage(t *testing.T) {
	gh := filepath.Join(t.TempDir(), "gh")
	// gh api --paginate prints one array per page.
	os.WriteFile(gh, []byte("#!/bin/sh\necho '[{\"id\":1,\"user\":{\"login\":\"a\",\"type\":\"User\"}}]'\necho '[{\"id\":2,\"user\":{\"login\":\"vercel[bot]\"}}]'\n"), 0o755)
	t.Setenv("SHIP_GH", gh)
	got, err := apiList[commentJSON](context.Background(), t.TempDir(), nil, "github.com", "repos/o/r/issues/1/comments")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 || got[0].User.bot() || !got[1].User.bot() {
		t.Fatalf("%+v", got)
	}
}

func TestApproved(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision string
		reviews  []string // author:state, oldest first
		want     bool
	}{
		{"decision wins", "REVIEW_REQUIRED", []string{"a:APPROVED"}, false},
		{"approved decision", "APPROVED", nil, true},
		{"no reviews", "", nil, false},
		{"one approval", "", []string{"a:APPROVED"}, true},
		{"comment after approval", "", []string{"a:APPROVED", "a:COMMENTED"}, true},
		{"changes requested by another", "", []string{"a:APPROVED", "b:CHANGES_REQUESTED"}, false},
		{"changes then approval", "", []string{"b:CHANGES_REQUESTED", "b:APPROVED"}, true},
		{"dismissed", "", []string{"a:APPROVED", "a:DISMISSED"}, false},
	} {
		pr := &PR{ReviewDecision: tc.decision}
		for _, r := range tc.reviews {
			author, state, _ := strings.Cut(r, ":")
			pr.Reviews = append(pr.Reviews, Review{Author: author, State: state})
		}
		if got := pr.Approved(); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}
