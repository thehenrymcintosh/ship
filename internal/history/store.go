package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
)

// Files in a pipeline's history dir. Both are append-only JSON lines, so
// they diff and merge sensibly in git.
const (
	VersionsFile = "versions.jsonl"
	FeedbackFile = "feedback.jsonl"
	ProposalsDir = "proposals"
	ReportsDir   = "reports"
)

// Version sources.
const (
	SourceFirst  = "first"  // the first time the pipeline ran
	SourceEdit   = "edit"   // a change noticed when a run started
	SourceRefine = "refine" // applied from `ship pipeline refine`
)

// Version is one entry of versions.jsonl.
type Version struct {
	Version   int       `json:"version"`
	Hash      string    `json:"hash"`
	At        time.Time `json:"at"`
	Source    string    `json:"source"`
	Summary   string    `json:"summary,omitempty"`
	Changed   []string  `json:"changed,omitempty"`   // parts that differ from the previous version
	Addresses []int     `json:"addresses,omitempty"` // feedback ids this version set out to fix
	Parts     []Part    `json:"parts"`
}

// Label is "v3 (a1f9c2d0)".
func (v Version) Label() string { return fmt.Sprintf("v%d (%s)", v.Version, short(v.Hash)) }

// Feedback sources.
const (
	FromCLI    = "cli"
	FromUI     = "ui"
	FromClaude = "claude"
	FromPR     = "pr"
)

// Record types in feedback.jsonl.
const (
	TypeFeedback = "feedback"
	TypeClose    = "close"
)

// Feedback is one entry of feedback.jsonl: a piece of feedback, or the
// closing of one (by id).
type Feedback struct {
	Type     string    `json:"type"`
	ID       int       `json:"id"`
	At       time.Time `json:"at"`
	Version  int       `json:"version,omitempty"`
	Hash     string    `json:"hash,omitempty"`
	Run      string    `json:"run,omitempty"`
	Step     string    `json:"step,omitempty"`
	Source   string    `json:"source,omitempty"`
	Author   string    `json:"author,omitempty"`
	Text     string    `json:"text,omitempty"`
	Link     string    `json:"link,omitempty"`
	SourceID string    `json:"source_id,omitempty"` // dedup key for imported feedback (PR comments)
	Note     string    `json:"note,omitempty"`      // on close records
}

// Item is a piece of feedback with its current status.
type Item struct {
	Feedback
	Status      string `json:"status"`                 // open | addressed | closed
	AddressedIn int    `json:"addressed_in,omitempty"` // version that addressed it
	CloseNote   string `json:"close_note,omitempty"`
}

// Store is one pipeline's history dir.
type Store struct {
	Dir     string // e.g. <repo>/.ship/history/<pipeline>
	LockDir string // where to keep the lock file (outside the repo)
}

// Open returns the store for dir; locks live under lockDir.
func Open(dir, lockDir string) *Store { return &Store{Dir: dir, LockDir: lockDir} }

// lock serializes writers (the daemon and CLI may both append).
func (s *Store) lock() (func(), error) {
	if err := os.MkdirAll(s.LockDir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(s.Dir))
	path := filepath.Join(s.LockDir, hex.EncodeToString(sum[:8])+".lock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		l, err := store.TryLock(path)
		if err == nil {
			return l.Unlock, nil
		}
		if !errors.Is(err, store.ErrLocked) || time.Now().After(deadline) {
			return nil, fmt.Errorf("locking %s: %w", s.Dir, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readLines[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

func (s *Store) appendLine(name string, v any) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Versions returns every version, oldest first.
func (s *Store) Versions() ([]Version, error) {
	return readLines[Version](filepath.Join(s.Dir, VersionsFile))
}

// Latest returns the newest version, or nil.
func (s *Store) Latest() (*Version, error) {
	vs, err := s.Versions()
	if err != nil || len(vs) == 0 {
		return nil, err
	}
	return &vs[len(vs)-1], nil
}

// Register records fp as a new version unless a version with that hash
// exists, in which case that version is returned. source, summary and
// addresses describe a new version.
func (s *Store) Register(fp Fingerprint, source, summary string, addresses []int) (Version, bool, error) {
	unlock, err := s.lock()
	if err != nil {
		return Version{}, false, err
	}
	defer unlock()
	vs, err := s.Versions()
	if err != nil {
		return Version{}, false, err
	}
	for _, v := range vs {
		if v.Hash == fp.Hash {
			return v, false, nil
		}
	}
	v := Version{Version: len(vs) + 1, Hash: fp.Hash, At: time.Now().UTC(), Source: source, Summary: summary, Addresses: addresses, Parts: fp.Parts}
	if len(vs) == 0 {
		v.Source = SourceFirst
	} else {
		v.Changed = Changed(vs[len(vs)-1].Parts, fp.Parts)
	}
	return v, true, s.appendLine(VersionsFile, v)
}

// records returns the raw feedback.jsonl entries.
func (s *Store) records() ([]Feedback, error) {
	return readLines[Feedback](filepath.Join(s.Dir, FeedbackFile))
}

// Add records a piece of feedback and assigns it the next id. Feedback
// with a SourceID already recorded is skipped (returned with ok=false).
func (s *Store) Add(f Feedback) (Feedback, bool, error) {
	if strings.TrimSpace(f.Text) == "" {
		return f, false, errors.New("feedback text is empty")
	}
	unlock, err := s.lock()
	if err != nil {
		return f, false, err
	}
	defer unlock()
	recs, err := s.records()
	if err != nil {
		return f, false, err
	}
	max := 0
	for _, r := range recs {
		if r.Type == TypeFeedback {
			if f.SourceID != "" && r.SourceID == f.SourceID {
				return r, false, nil
			}
			if r.ID > max {
				max = r.ID
			}
		}
	}
	f.Type, f.ID = TypeFeedback, max+1
	if f.At.IsZero() {
		f.At = time.Now().UTC()
	}
	return f, true, s.appendLine(FeedbackFile, f)
}

// Close marks feedback as dealt with outside a refinement.
func (s *Store) Close(id int, note string) error {
	items, err := s.Items()
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.ID == id {
			unlock, err := s.lock()
			if err != nil {
				return err
			}
			defer unlock()
			return s.appendLine(FeedbackFile, Feedback{Type: TypeClose, ID: id, At: time.Now().UTC(), Note: note})
		}
	}
	return fmt.Errorf("no feedback #%d", id)
}

// Items returns all feedback with its status, oldest first.
func (s *Store) Items() ([]Item, error) {
	recs, err := s.records()
	if err != nil {
		return nil, err
	}
	vs, err := s.Versions()
	if err != nil {
		return nil, err
	}
	addressed := map[int]int{}
	for _, v := range vs {
		for _, id := range v.Addresses {
			if _, ok := addressed[id]; !ok {
				addressed[id] = v.Version
			}
		}
	}
	closed := map[int]string{}
	for _, r := range recs {
		if r.Type == TypeClose {
			closed[r.ID] = r.Note
		}
	}
	var out []Item
	for _, r := range recs {
		if r.Type != TypeFeedback {
			continue
		}
		it := Item{Feedback: r, Status: "open"}
		if v, ok := addressed[r.ID]; ok {
			it.Status, it.AddressedIn = "addressed", v
		} else if note, ok := closed[r.ID]; ok {
			it.Status, it.CloseNote = "closed", note
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
