package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
	gitws "github.com/thehenrymcintosh/ship/internal/workspace/git"
)

// Home is the user-level dir (~/.ship), or "" when unknown.
func (e *Engine) Home() string {
	if e.o.GlobalPipelines == "" {
		return ""
	}
	return filepath.Dir(e.o.GlobalPipelines)
}

// HistoryDir is where a pipeline's history lives: beside the pipeline, in
// <repo>/.ship/history/<name> for repo pipelines (committed with them) or
// ~/.ship/history/<name> for global ones.
func HistoryDir(l *pipeline.Loader, repo, home, name string) string {
	if repo != "" && (l.Dir(name) == PipelinesDir(repo) || home == "") {
		return filepath.Join(repo, brand.Dir, "history", name)
	}
	if home != "" && l.Dir(name) != "" {
		return filepath.Join(home, "history", name)
	}
	return filepath.Join(repo, brand.Dir, "history", name)
}

// LockDir keeps history locks out of the repo.
func LockDir(home string) string {
	if home == "" {
		return filepath.Join(os.TempDir(), brand.Name+"-locks")
	}
	return filepath.Join(home, "locks")
}

// HistoryStore opens a pipeline's history.
func (e *Engine) HistoryStore(dir string) *history.Store { return history.Open(dir, LockDir(e.Home())) }

// Fingerprint computes a pipeline's version fingerprint.
func (e *Engine) Fingerprint(repo string, closure []*pipeline.File) history.Fingerprint {
	return history.Compute(history.Inputs{Pipelines: closure, Repo: repo, Home: e.Home(), ClaudeDir: e.o.ClaudeDir})
}

// RegisterVersion records the pipeline's current fingerprint as a version
// (or finds the existing one).
func (e *Engine) RegisterVersion(repo string, closure []*pipeline.File, dir, source, summary string, addresses []int) (history.Version, error) {
	v, _, err := e.HistoryStore(dir).Register(e.Fingerprint(repo, closure), source, summary, addresses)
	return v, err
}

// GitAuthor returns the repo's git user.name, for attributing feedback.
func GitAuthor(repo string) string {
	out, err := exec.Command("git", "-C", repo, "config", "user.name").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RecordFeedback stores feedback about a run (optionally one of its steps)
// against the pipeline version that run used. added is false when the
// feedback was already recorded (same SourceID).
func RecordFeedback(snap *store.RunSnapshot, home string, f history.Feedback) (fb history.Feedback, added bool, err error) {
	dir := snap.HistoryDir
	if dir == "" {
		// Runs from before versioning: fall back to the repo's history dir.
		dir = filepath.Join(snap.Repo, brand.Dir, "history", snap.Pipeline)
	}
	f.Run, f.Version, f.Hash = snap.ID, snap.PipelineVersion, snap.PipelineHash
	if f.Author == "" {
		f.Author = GitAuthor(snap.Repo)
	}
	return history.Open(dir, LockDir(home)).Add(f)
}

// FeedbackForRun returns the feedback recorded about one run.
func FeedbackForRun(snap *store.RunSnapshot, home string) ([]history.Item, error) {
	dir := snap.HistoryDir
	if dir == "" {
		dir = filepath.Join(snap.Repo, brand.Dir, "history", snap.Pipeline)
	}
	items, err := history.Open(dir, LockDir(home)).Items()
	if err != nil {
		return nil, err
	}
	var out []history.Item
	for _, it := range items {
		if it.Run == snap.ID {
			out = append(out, it)
		}
	}
	return out, nil
}

// AddFeedback records feedback about a run through the engine (the daemon
// API and UI use this).
func (e *Engine) AddFeedback(id, step, text, source, link, sourceID, author string) (history.Feedback, error) {
	snap, err := e.Snapshot(id)
	if err != nil {
		return history.Feedback{}, err
	}
	if step != "" {
		p, err := e.RunPipeline(id)
		if err == nil && p.Steps[step] == nil {
			return history.Feedback{}, invalid("run %s's pipeline has no step %q", id, step)
		}
	}
	if strings.TrimSpace(text) == "" {
		return history.Feedback{}, invalid("feedback text is empty")
	}
	fb, _, err := RecordFeedback(snap, e.Home(), history.Feedback{Step: step, Text: strings.TrimSpace(text), Source: source, Link: link, SourceID: sourceID, Author: author})
	return fb, err
}

// PipelineHistory resolves a pipeline by name for a repo, registering its
// current version, and returns its history store and closure.
func (e *Engine) PipelineHistory(repo, name string) (*history.Store, []*pipeline.File, history.Version, error) {
	l := e.Loader(repo)
	closure, err := l.Closure(name)
	if err != nil {
		return nil, nil, history.Version{}, err
	}
	dir := HistoryDir(l, repo, e.Home(), name)
	v, err := e.RegisterVersion(repo, closure, dir, history.SourceEdit, "", nil)
	if err != nil {
		return nil, nil, history.Version{}, err
	}
	return e.HistoryStore(dir), closure, v, nil
}

// MainCheckout maps a path to its repo's main checkout.
func MainCheckout(path string) (string, error) {
	repo, err := gitws.MainCheckout(context.Background(), path)
	if err != nil {
		return "", fmt.Errorf("%s isn't in a git repo", path)
	}
	return repo, nil
}

// PipelineStatus describes a pipeline's current version without recording
// anything: its label ("v3 (a1f9c2d0)", or "unversioned" if it has changed
// since it last ran) and how much feedback on it is open.
func (e *Engine) PipelineStatus(repo, name string) (label string, open int) {
	l := e.Loader(repo)
	closure, err := l.Closure(name)
	if err != nil {
		return "", 0
	}
	hs := e.HistoryStore(HistoryDir(l, repo, e.Home(), name))
	fp := e.Fingerprint(repo, closure)
	label = "unversioned (" + fp.Short() + ")"
	if vs, err := hs.Versions(); err == nil {
		for _, v := range vs {
			if v.Hash == fp.Hash {
				label = v.Label()
			}
		}
	}
	if items, err := hs.Items(); err == nil {
		for _, it := range items {
			if it.Status == "open" {
				open++
			}
		}
	}
	return label, open
}

// UnsyncedFiles lists the skills, rules and scripts a pipeline uses that a
// run's worktree won't see as they are in the checkout: runs check out the
// base branch, so files that are uncommitted, or committed but not on the
// base, are invisible to the run's agents and scripts.
func (e *Engine) UnsyncedFiles(ctx context.Context, repo, base string, closure []*pipeline.File) []string {
	var out []string
	for _, p := range e.Fingerprint(repo, closure).Parts {
		if p.Missing || p.Path == "" || p.Kind == history.KindPipeline {
			continue
		}
		rel, err := filepath.Rel(repo, p.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue // user-level files are read live, not from the worktree
		}
		missing, differs := compareWithRef(ctx, repo, base, rel)
		switch {
		case missing:
			out = append(out, fmt.Sprintf("%s %s isn't committed on %s, so this run won't see it (commit it there)", p.Kind, rel, base))
		case differs:
			out = append(out, fmt.Sprintf("%s %s differs from %s (uncommitted or not merged there), so this run uses %s's version", p.Kind, rel, base, base))
		}
	}
	return out
}

// compareWithRef compares the checkout's copy of rel (a file or a folder)
// with ref's: missing when ref has none of it, differs when any file's
// content or the set of files isn't the same.
func compareWithRef(ctx context.Context, repo, ref, rel string) (missing, differs bool) {
	listing, err := gitws.Git(ctx, repo, "ls-tree", "-r", ref, "--", rel)
	if err != nil {
		return false, false // can't tell (e.g. ref isn't there yet): don't warn
	}
	want := map[string]string{} // path → blob hash
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		meta, path, ok := strings.Cut(line, "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 {
			want[path] = f[2]
		}
	}
	if len(want) == 0 {
		return true, false
	}
	var have []string
	_ = filepath.WalkDir(filepath.Join(repo, rel), func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if r, err := filepath.Rel(repo, path); err == nil {
				have = append(have, filepath.ToSlash(r))
			}
		}
		return nil
	})
	if len(have) != len(want) {
		return false, true
	}
	hashes, err := gitws.Git(ctx, repo, append([]string{"hash-object", "--"}, have...)...)
	if err != nil {
		return false, true
	}
	for i, h := range strings.Fields(hashes) {
		if i >= len(have) || want[have[i]] != h {
			return false, true
		}
	}
	return false, false
}
