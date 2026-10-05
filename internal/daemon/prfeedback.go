package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// PR comments starting with "ship:" are feedback on the run that opened
// the PR. Ordinary review comments stay instructions for the agents.
var prFeedbackRE = regexp.MustCompile(`(?is)^\s*` + regexp.QuoteMeta(brand.Name) + `:\s*(.+?)\s*$`)

// prPoller imports PR feedback via the gh CLI.
type prPoller struct {
	st     *store.Store
	home   string
	gh     string // gh binary
	log    *slog.Logger
	maxAge time.Duration // how long after finishing a run's PR is still watched

	mu  sync.Mutex
	prs map[string]int // run id → PR number (found)
}

func newPRPoller(st *store.Store, home string, log *slog.Logger) *prPoller {
	return &prPoller{st: st, home: home, gh: "gh", log: log, maxAge: 14 * 24 * time.Hour, prs: map[string]int{}}
}

func (p *prPoller) run(ctx context.Context, every time.Duration) {
	if _, err := exec.LookPath(p.gh); err != nil {
		p.log.Info("gh not installed: PR comments won't be imported as feedback")
		return
	}
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.once(ctx)
			t.Reset(every)
		}
	}
}

func (p *prPoller) ghJSON(ctx context.Context, dir string, out any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.gh, args...)
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("gh %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return err
	}
	return json.Unmarshal(b, out)
}

// once checks every recent run's PR for new feedback. It returns how many
// pieces of feedback it recorded.
func (p *prPoller) once(ctx context.Context) int {
	snaps, err := p.st.List()
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range snaps {
		if s.Branch == "" || s.Repo == "" || s.Provider == "none" {
			continue
		}
		if s.Status.Terminal() && (s.FinishedAt == nil || time.Since(*s.FinishedAt) > p.maxAge) {
			continue
		}
		num, url := p.findPR(ctx, s)
		if num == 0 {
			continue
		}
		n += p.importComments(ctx, s, num, url)
	}
	return n
}

func (p *prPoller) findPR(ctx context.Context, s *store.RunSnapshot) (int, string) {
	p.mu.Lock()
	num := p.prs[s.ID]
	p.mu.Unlock()
	if num > 0 {
		return num, ""
	}
	var list []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if err := p.ghJSON(ctx, s.Repo, &list, "pr", "list", "--head", s.Branch, "--state", "all", "--json", "number,url", "--limit", "1"); err != nil || len(list) == 0 {
		return 0, ""
	}
	p.mu.Lock()
	p.prs[s.ID] = list[0].Number
	p.mu.Unlock()
	return list[0].Number, list[0].URL
}

type ghAuthor struct {
	Login string `json:"login"`
}

func (p *prPoller) importComments(ctx context.Context, s *store.RunSnapshot, num int, prURL string) int {
	var view struct {
		URL      string `json:"url"`
		Comments []struct {
			ID     string   `json:"id"`
			Author ghAuthor `json:"author"`
			Body   string   `json:"body"`
			URL    string   `json:"url"`
		} `json:"comments"`
		Reviews []struct {
			ID     string   `json:"id"`
			Author ghAuthor `json:"author"`
			Body   string   `json:"body"`
		} `json:"reviews"`
	}
	n := fmt.Sprint(num)
	if err := p.ghJSON(ctx, s.Repo, &view, "pr", "view", n, "--json", "url,comments,reviews"); err != nil {
		p.log.Debug("pr feedback", "run", s.ID, "err", err)
		return 0
	}
	type found struct{ text, author, link, id string }
	var all []found
	for _, c := range view.Comments {
		all = append(all, found{c.Body, c.Author.Login, c.URL, "gh-comment:" + c.ID})
	}
	for _, r := range view.Reviews {
		all = append(all, found{r.Body, r.Author.Login, view.URL, "gh-review:" + r.ID})
	}
	var inline []struct {
		ID      int64    `json:"id"`
		Body    string   `json:"body"`
		HTMLURL string   `json:"html_url"`
		User    ghAuthor `json:"user"`
		Path    string   `json:"path"`
	}
	if err := p.ghJSON(ctx, s.Repo, &inline, "api", "repos/{owner}/{repo}/pulls/"+n+"/comments"); err == nil {
		for _, c := range inline {
			all = append(all, found{c.Body, c.User.Login, c.HTMLURL, fmt.Sprintf("gh-inline:%d", c.ID)})
		}
	}
	added := 0
	for _, f := range all {
		m := prFeedbackRE.FindStringSubmatch(f.text)
		if m == nil {
			continue
		}
		link := f.link
		if link == "" {
			link = prURL
		}
		_, isNew, err := engine.RecordFeedback(s, p.home, history.Feedback{Text: m[1], Source: history.FromPR, Author: f.author, Link: link, SourceID: f.id})
		if err != nil {
			p.log.Warn("recording PR feedback", "run", s.ID, "err", err)
			continue
		}
		if isNew {
			added++
		}
	}
	return added
}
