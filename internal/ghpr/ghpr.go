// Package ghpr reads a GitHub pull request's state, checks and review
// comments through the gh CLI, for the `pr:` step.
package ghpr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/brand"
)

// Bin is the gh binary: $SHIP_GH (for tests) or "gh" on PATH.
func Bin() string {
	if b := os.Getenv(brand.EnvPrefix + "GH"); b != "" {
		return b
	}
	return "gh"
}

// ErrNoPR means no pull request exists for the ref yet.
var ErrNoPR = errors.New("no pull request yet")

func run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, Bin(), args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		if strings.Contains(msg, "no pull requests found") || strings.Contains(msg, "Could not resolve to a PullRequest") {
			return nil, ErrNoPR
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// Author is a GitHub user.
type Author struct {
	Login string `json:"login"`
}

// Check is one entry of a PR's status check rollup (a check run or a
// commit status).
type Check struct {
	Typename     string    `json:"__typename"`
	Name         string    `json:"name"`
	Context      string    `json:"context"` // status contexts
	Status       string    `json:"status"`  // check runs: QUEUED, IN_PROGRESS, COMPLETED…
	Conclusion   string    `json:"conclusion"`
	State        string    `json:"state"` // status contexts: SUCCESS, FAILURE, PENDING, ERROR…
	DetailsURL   string    `json:"detailsUrl"`
	TargetURL    string    `json:"targetUrl"`
	WorkflowName string    `json:"workflowName"`
	StartedAt    time.Time `json:"startedAt"`
}

// Label is the check's display name.
func (c Check) Label() string {
	n := c.Name
	if n == "" {
		n = c.Context
	}
	if c.WorkflowName != "" && !strings.Contains(n, c.WorkflowName) {
		return c.WorkflowName + " / " + n
	}
	return n
}

// URL links to the check's details.
func (c Check) URL() string {
	if c.DetailsURL != "" {
		return c.DetailsURL
	}
	return c.TargetURL
}

// Buckets.
const (
	Pass      = "pass"
	Fail      = "fail"
	Pending   = "pending"
	Cancelled = "cancelled"
	Skipped   = "skipped"
)

// Bucket classifies a check.
func (c Check) Bucket() string {
	if c.Typename == "StatusContext" || (c.Status == "" && c.State != "") {
		switch c.State {
		case "SUCCESS":
			return Pass
		case "FAILURE", "ERROR":
			return Fail
		}
		return Pending
	}
	if c.Status != "COMPLETED" {
		return Pending
	}
	switch c.Conclusion {
	case "SUCCESS", "NEUTRAL":
		return Pass
	case "SKIPPED":
		return Skipped
	case "CANCELLED":
		return Cancelled
	case "FAILURE", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
		return Fail
	}
	return Pending
}

var runIDRE = regexp.MustCompile(`/actions/runs/(\d+)`)

// RunID is the GitHub Actions run behind a check, if any.
func (c Check) RunID() string {
	if m := runIDRE.FindStringSubmatch(c.URL()); m != nil {
		return m[1]
	}
	return ""
}

// Comment is one piece of review feedback: a conversation comment, a review
// summary, or an inline code comment.
type Comment struct {
	ID        string // "ic:…", "rv:…", "rc:…"
	Kind      string // comment | review | inline
	Author    string
	Bot       bool // the author is an app or integration
	Body      string
	URL       string
	Path      string
	Line      int
	State     string // reviews: CHANGES_REQUESTED, COMMENTED, APPROVED
	CreatedAt time.Time
}

// PR is one observation of a pull request.
type PR struct {
	Number         int
	URL            string
	State          string // OPEN, CLOSED, MERGED
	HeadSHA        string
	ReviewDecision string
	IsDraft        bool
	Checks         []Check
	Comments       []Comment
	Reviews        []Review // every review, oldest first
}

// Review is one submitted review's verdict.
type Review struct {
	Author      string
	State       string // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	SubmittedAt time.Time
}

// Approved reports whether the PR is approved. GitHub only sets a review
// decision when branch protection requires reviews; without one, it's
// approved when someone's latest verdict approves it and nobody's latest
// verdict requests changes.
func (pr *PR) Approved() bool {
	if pr.ReviewDecision != "" {
		return pr.ReviewDecision == "APPROVED"
	}
	verdict := map[string]string{}
	for _, r := range pr.Reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			verdict[r.Author] = r.State
		}
	}
	approved := false
	for _, s := range verdict {
		switch s {
		case "CHANGES_REQUESTED":
			return false
		case "APPROVED":
			approved = true
		}
	}
	return approved
}

type viewJSON struct {
	Number            int     `json:"number"`
	URL               string  `json:"url"`
	State             string  `json:"state"`
	HeadRefOid        string  `json:"headRefOid"`
	ReviewDecision    string  `json:"reviewDecision"`
	IsDraft           bool    `json:"isDraft"`
	StatusCheckRollup []Check `json:"statusCheckRollup"`
	Reviews           []struct {
		ID          string    `json:"id"`
		Author      Author    `json:"author"`
		Body        string    `json:"body"`
		State       string    `json:"state"`
		SubmittedAt time.Time `json:"submittedAt"`
	} `json:"reviews"`
}

// commentJSON is a conversation or inline comment from the REST API, which
// (unlike gh pr view) says whether the author is a bot.
type commentJSON struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
	OrigLine  int       `json:"original_line"`
	User      restUser  `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

type restUser struct {
	Login string `json:"login"`
	Type  string `json:"type"` // User, Bot…
}

func (u restUser) bot() bool {
	return u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]")
}

// apiList reads every page of a REST list endpoint; gh prints one JSON array
// per page.
func apiList[T any](ctx context.Context, dir string, env []string, path string) ([]T, error) {
	out, err := run(ctx, dir, env, "api", "--paginate", path)
	if err != nil {
		return nil, err
	}
	var all []T
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var page []T
		if err := dec.Decode(&page); err == io.EOF {
			return all, nil
		} else if err != nil {
			return nil, fmt.Errorf("gh api %s: unexpected output: %v", path, err)
		}
		all = append(all, page...)
	}
}

// View reads a PR (by number, URL or branch) with its checks and comments.
func View(ctx context.Context, dir string, env []string, ref string) (*PR, error) {
	out, err := run(ctx, dir, env, "pr", "view", ref, "--json", "number,url,state,headRefOid,reviewDecision,isDraft,statusCheckRollup,reviews")
	if err != nil {
		return nil, err
	}
	var v viewJSON
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("gh pr view: unexpected output: %v", err)
	}
	pr := &PR{Number: v.Number, URL: v.URL, State: v.State, HeadSHA: v.HeadRefOid, ReviewDecision: v.ReviewDecision, IsDraft: v.IsDraft, Checks: Latest(v.StatusCheckRollup)}
	for _, r := range v.Reviews {
		pr.Reviews = append(pr.Reviews, Review{Author: r.Author.Login, State: r.State, SubmittedAt: r.SubmittedAt})
		if strings.TrimSpace(r.Body) == "" && r.State != "CHANGES_REQUESTED" {
			continue
		}
		pr.Comments = append(pr.Comments, Comment{ID: "rv:" + r.ID, Kind: "review", Author: r.Author.Login, Body: r.Body, URL: v.URL, State: r.State, CreatedAt: r.SubmittedAt})
	}
	// Comments come from the REST API, which flags bot authors. A failure
	// here only means they're picked up on a later poll.
	if conv, err := apiList[commentJSON](ctx, dir, env, fmt.Sprintf("repos/{owner}/{repo}/issues/%d/comments?per_page=100", v.Number)); err == nil {
		for _, c := range conv {
			pr.Comments = append(pr.Comments, Comment{ID: fmt.Sprintf("ic:%d", c.ID), Kind: "comment", Author: c.User.Login, Bot: c.User.bot(), Body: c.Body, URL: c.HTMLURL, CreatedAt: c.CreatedAt})
		}
	}
	if inline, err := apiList[commentJSON](ctx, dir, env, fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/comments?per_page=100", v.Number)); err == nil {
		for _, c := range inline {
			line := c.Line
			if line == 0 {
				line = c.OrigLine
			}
			pr.Comments = append(pr.Comments, Comment{ID: fmt.Sprintf("rc:%d", c.ID), Kind: "inline", Author: c.User.Login, Bot: c.User.bot(), Body: c.Body, URL: c.HTMLURL, Path: c.Path, Line: line, CreatedAt: c.CreatedAt})
		}
	}
	sort.SliceStable(pr.Comments, func(i, j int) bool { return pr.Comments[i].CreatedAt.Before(pr.Comments[j].CreatedAt) })
	sort.SliceStable(pr.Reviews, func(i, j int) bool { return pr.Reviews[i].SubmittedAt.Before(pr.Reviews[j].SubmittedAt) })
	return pr, nil
}

// Latest keeps only the newest run of each same-named check: a rollup lists
// every run a commit ever had, and a superseded failure must not count.
func Latest(checks []Check) []Check {
	newest := map[string]int{}
	var order []string
	for i, c := range checks {
		key := c.Typename + "\x00" + c.WorkflowName + "\x00" + c.Name + "\x00" + c.Context
		j, ok := newest[key]
		if !ok {
			order = append(order, key)
			newest[key] = i
			continue
		}
		if c.StartedAt.After(checks[j].StartedAt) {
			newest[key] = i
		}
	}
	out := make([]Check, 0, len(order))
	for _, k := range order {
		out = append(out, checks[newest[k]])
	}
	return out
}

// FailedLog returns the tail of a GitHub Actions run's failed-step logs.
func FailedLog(ctx context.Context, dir string, env []string, runID string, lines int) string {
	out, err := run(ctx, dir, env, "run", "view", runID, "--log-failed")
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// Rerun re-runs a GitHub Actions run's failed or cancelled jobs.
func Rerun(ctx context.Context, dir string, env []string, runID string) error {
	_, err := run(ctx, dir, env, "run", "rerun", runID, "--failed")
	return err
}
