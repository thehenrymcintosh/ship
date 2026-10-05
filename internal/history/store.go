package history

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/store"
)

// A pipeline's history dir (a pipeline folder, or .ship/history/<name>
// for a single-file pipeline). Every record is its own file, written once
// and never changed, so people committing the same pipeline's history from
// different machines never edit the same file:
//
//	versions/<hash12>/version.json     what the version is (the same wherever it's recorded)
//	versions/<hash12>/files/…          a frozen copy of every part
//	versions/<hash12>/seen/<id>.json   who recorded it, when and why
//	feedback/<key>.json                one piece of feedback
//	feedback/<key>.closed-<id>.json    that feedback marked as dealt with
//	runs/<run-id>.json                 a finished run's stats
//	proposals/, reports/               refine and report output
//
// versions.jsonl and feedback.jsonl from older ships are converted when
// the history is opened (they're left in place, and can be deleted).
const (
	VersionsDir  = "versions"
	FeedbackDir  = "feedback"
	RunsDir      = "runs"
	ProposalsDir = "proposals"
	ReportsDir   = "reports"

	versionFileName = "version.json"
	seenDir         = "seen"
	filesDir        = "files"

	legacyVersions = "versions.jsonl"
	legacyFeedback = "feedback.jsonl"
)

// Subdirs are the history's own dirs (inside a pipeline folder, they sit
// beside pipeline.yml and skills/).
var Subdirs = []string{VersionsDir, FeedbackDir, RunsDir, ProposalsDir, ReportsDir}

// Version sources.
const (
	SourceFirst  = "first"  // the first time the pipeline ran
	SourceEdit   = "edit"   // a change noticed when a run started
	SourceRefine = "refine" // applied from `ship pipeline refine`
)

// Version is one version of a pipeline. Version (v1, v2…), Changed and
// Addresses are worked out when read: versions are numbered in the order
// they were first seen.
type Version struct {
	Version     int       `json:"version"`
	Hash        string    `json:"hash"`
	At          time.Time `json:"at"` // first seen
	Source      string    `json:"source"`
	Summary     string    `json:"summary,omitempty"`
	Changed     []string  `json:"changed,omitempty"`   // parts that differ from the parent (or the previous version)
	Addresses   []int     `json:"addresses,omitempty"` // feedback this version set out to fix
	AddressKeys []string  `json:"address_keys,omitempty"`
	Parent      string    `json:"parent,omitempty"` // the version it was made from, when known
	Author      string    `json:"author,omitempty"`
	Parts       []Part    `json:"parts"`
	// Frozen is set when every part found has a saved copy, so the
	// version can be restored.
	Frozen bool   `json:"frozen"`
	Dir    string `json:"-"`
}

// Label is "v3 (a1f9c2d0)".
func (v Version) Label() string { return fmt.Sprintf("v%d (%s)", v.Version, short(v.Hash)) }

// versionFile is version.json: only what the hash determines, so it's
// identical whoever writes it.
type versionFile struct {
	Hash  string `json:"hash"`
	Parts []Part `json:"parts"`
}

// seenFile is one sighting of a version.
type seenFile struct {
	At        time.Time         `json:"at"`
	Source    string            `json:"source,omitempty"`
	Summary   string            `json:"summary,omitempty"`
	Addresses []string          `json:"addresses,omitempty"` // feedback keys
	Parent    string            `json:"parent,omitempty"`
	Author    string            `json:"author,omitempty"`
	Locations map[string]string `json:"locations,omitempty"` // "kind name" → Part.Location
}

// Feedback sources.
const (
	FromCLI    = "cli"
	FromUI     = "ui"
	FromClaude = "claude"
	FromPR     = "pr"
)

// Record types.
const (
	TypeFeedback = "feedback"
	TypeClose    = "close"
)

// Feedback is a piece of feedback, or the closing of one. Key identifies
// it for good; ID (#1, #2…) is its place in the order feedback was given,
// worked out when read.
type Feedback struct {
	Type     string    `json:"type"`
	Key      string    `json:"key,omitempty"`
	ID       int       `json:"id,omitempty"`
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
	Dir     string // a pipeline folder, or <repo>/.ship/history/<pipeline>
	LockDir string // where to keep the lock file (outside the repo)
	Author  string // recorded with new versions
}

// Open returns the store for dir; locks live under lockDir.
func Open(dir, lockDir string) *Store { return &Store{Dir: dir, LockDir: lockDir} }

// lock serializes writers on this machine (the daemon and CLI).
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

// NewID is a unique, time-ordered id for a record file:
// 20261005T161253Z-a1b2c3.
func NewID(t time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

// Hash12 is the dir name of a version.
func Hash12(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// writeOnce writes v as JSON to path unless it exists.
func writeOnce(path string, v any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON[T any](path string) (T, error) {
	var v T
	b, err := os.ReadFile(path)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

// --- versions ----------------------------------------------------------------

// CopyRel is where a version keeps a part's copy, under its files/ dir.
func CopyRel(p Part) string {
	clean := func(s string) string {
		var out []string
		for _, seg := range strings.Split(filepath.ToSlash(s), "/") {
			seg = strings.TrimLeft(seg, "$")
			switch seg {
			case "", ".":
			case "..":
				out = append(out, "_")
			default:
				out = append(out, seg)
			}
		}
		return strings.Join(out, "/")
	}
	switch p.Kind {
	case KindPipeline:
		return KindPipeline + "/" + clean(p.Name) + ".yml"
	case KindCommand:
		return KindCommand + "/" + clean(p.Name) + ".md"
	}
	return p.Kind + "/" + clean(p.Name)
}

// Versions returns every version, in the order first seen.
func (s *Store) Versions() ([]Version, error) {
	if err := s.migrate(false); err != nil {
		return nil, err
	}
	fb, err := s.feedbackRecords()
	if err != nil {
		return nil, err
	}
	return s.versions(fb)
}

func (s *Store) versions(fb []Feedback) ([]Version, error) {
	root := filepath.Join(s.Dir, VersionsDir)
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var out []Version
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		vf, err := readJSON[versionFile](filepath.Join(dir, versionFileName))
		if err != nil {
			continue // half-copied or foreign: skip
		}
		v := Version{Hash: vf.Hash, Parts: vf.Parts, Dir: dir, Frozen: true}
		seen, _ := filepath.Glob(filepath.Join(dir, seenDir, "*.json"))
		sort.Strings(seen)
		var first *seenFile
		keys := map[string]bool{}
		for _, p := range seen {
			sf, err := readJSON[seenFile](p)
			if err != nil {
				continue
			}
			if first == nil || sf.At.Before(first.At) {
				f := sf
				first = &f
			}
			if sf.Summary != "" && (v.Summary == "" || sf.Source == SourceRefine) {
				v.Summary = sf.Summary
			}
			for _, k := range sf.Addresses {
				if !keys[k] {
					keys[k] = true
					v.AddressKeys = append(v.AddressKeys, k)
				}
			}
			if sf.Source == SourceRefine {
				v.Source = SourceRefine
			}
		}
		if first != nil {
			v.At, v.Parent, v.Author = first.At, first.Parent, first.Author
			if v.Source == "" {
				v.Source = first.Source
			}
		}
		for i := range v.Parts {
			p := &v.Parts[i]
			if first != nil {
				p.Location = first.Locations[p.Kind+" "+p.Name]
			}
			if p.Missing {
				continue
			}
			rel := CopyRel(*p)
			if _, err := os.Stat(filepath.Join(dir, filesDir, filepath.FromSlash(rel))); err == nil {
				p.Copy = filesDir + "/" + rel
			} else {
				v.Frozen = false
			}
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Hash < out[j].Hash
	})
	ids := feedbackIDs(fb)
	idx := map[string]int{}
	for i := range out {
		out[i].Version = i + 1
		idx[out[i].Hash] = i
	}
	for i := range out {
		v := &out[i]
		if j, ok := idx[v.Parent]; ok && j != i {
			v.Changed = Changed(out[j].Parts, v.Parts)
		} else if i > 0 {
			v.Changed = Changed(out[i-1].Parts, v.Parts)
		}
		for _, k := range v.AddressKeys {
			if id, ok := ids[k]; ok {
				v.Addresses = append(v.Addresses, id)
			}
		}
		sort.Ints(v.Addresses)
	}
	return out, nil
}

// Latest returns the newest version, or nil.
func (s *Store) Latest() (*Version, error) {
	vs, err := s.Versions()
	if err != nil || len(vs) == 0 {
		return nil, err
	}
	return &vs[len(vs)-1], nil
}

// Find resolves "v3", "3" or a hash prefix (4+ characters) to a version.
func (s *Store) Find(ref string) (*Version, error) {
	vs, err := s.Versions()
	if err != nil {
		return nil, err
	}
	return FindIn(vs, ref)
}

// FindIn resolves a version reference among vs (see Store.Find).
func FindIn(vs []Version, ref string) (*Version, error) {
	r := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ref)), "v")
	if n, err := strconv.Atoi(r); err == nil && len(r) < 4 {
		if n >= 1 && n <= len(vs) {
			return &vs[n-1], nil
		}
		return nil, fmt.Errorf("no version v%d (there are %d)", n, len(vs))
	}
	r = strings.TrimSpace(strings.ToLower(ref))
	var found *Version
	if len(r) >= 4 {
		for i := range vs {
			if strings.HasPrefix(vs[i].Hash, r) {
				if found != nil {
					return nil, fmt.Errorf("%q matches more than one version", ref)
				}
				found = &vs[i]
			}
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no version %q (use v1, v2… or a hash)", ref)
	}
	return found, nil
}

// Register records fp as a version unless it's been seen, in which case
// that version is returned. source, summary and addresses (feedback ids)
// describe it; for a version already seen, a summary or addresses are
// recorded as another sighting. A new version keeps a copy of every part.
func (s *Store) Register(fp Fingerprint, source, summary string, addresses []int) (Version, bool, error) {
	unlock, err := s.lock()
	if err != nil {
		return Version{}, false, err
	}
	defer unlock()
	if err := s.migrate(true); err != nil {
		return Version{}, false, err
	}
	fb, err := s.feedbackRecords()
	if err != nil {
		return Version{}, false, err
	}
	vs, err := s.versions(fb)
	if err != nil {
		return Version{}, false, err
	}
	keyOf := map[int]string{}
	for k, id := range feedbackIDs(fb) {
		keyOf[id] = k
	}
	var keys []string
	for _, id := range addresses {
		if k, ok := keyOf[id]; ok {
			keys = append(keys, k)
		}
	}
	now := time.Now().UTC()
	sf := seenFile{At: now, Source: source, Summary: summary, Addresses: keys, Author: s.Author, Locations: map[string]string{}}
	for _, p := range fp.Parts {
		if p.Path != "" {
			if loc := Locate(fp.Roots, p.Path); loc != "" {
				sf.Locations[p.Kind+" "+p.Name] = loc
			}
		}
	}
	dir := filepath.Join(s.Dir, VersionsDir, Hash12(fp.Hash))
	_, statErr := os.Stat(filepath.Join(dir, versionFileName))
	created := statErr != nil
	switch {
	case created:
		if len(vs) == 0 {
			sf.Source = SourceFirst
		} else {
			sf.Parent = vs[len(vs)-1].Hash
		}
		if err := s.writeVersion(dir, fp, sf); err != nil {
			return Version{}, false, err
		}
	case summary != "" || len(keys) > 0:
		if err := writeOnce(filepath.Join(dir, seenDir, NewID(now)+".json"), sf); err != nil {
			return Version{}, false, err
		}
	}
	if vs, err = s.versions(fb); err != nil {
		return Version{}, false, err
	}
	for _, v := range vs {
		if v.Hash == fp.Hash {
			return v, created, nil
		}
	}
	return Version{}, false, fmt.Errorf("version %s wasn't recorded", short(fp.Hash))
}

// writeVersion assembles a version in a temporary dir and moves it into
// place, so a version dir is never half-written.
func (s *Store) writeVersion(dir string, fp Fingerprint, sf seenFile) error {
	tmp := filepath.Join(filepath.Dir(dir), ".tmp-"+NewID(time.Now()))
	defer os.RemoveAll(tmp)
	vf := versionFile{Hash: fp.Hash}
	for _, p := range fp.Parts {
		if !p.Missing && p.Path != "" {
			if err := copyPart(p.Path, filepath.Join(tmp, filesDir, filepath.FromSlash(CopyRel(p)))); err != nil {
				return fmt.Errorf("saving a copy of %s %s: %w", p.Kind, p.Name, err)
			}
		}
		vf.Parts = append(vf.Parts, Part{Kind: p.Kind, Name: p.Name, Hash: p.Hash, Missing: p.Missing})
	}
	if err := writeOnce(filepath.Join(tmp, versionFileName), vf); err != nil {
		return err
	}
	if err := writeOnce(filepath.Join(tmp, seenDir, NewID(sf.At)+".json"), sf); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, serr := os.Stat(filepath.Join(dir, versionFileName)); serr == nil {
			// Someone recorded it meanwhile: add our sighting.
			return writeOnce(filepath.Join(dir, seenDir, NewID(sf.At)+".json"), sf)
		}
		return err
	}
	return nil
}

// copyPart copies a file, or a dir's files (skipping dotfiles, as the
// fingerprint does), to dest.
func copyPart(src, dest string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return copyFile(src, dest, st.Mode())
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != src {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(p, filepath.Join(dest, rel), info.Mode())
	})
}

func copyFile(src, dest string, mode fs.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, b, mode.Perm()|0o600)
}

// --- feedback ----------------------------------------------------------------

// feedbackRecords reads every feedback and close record.
func (s *Store) feedbackRecords() ([]Feedback, error) {
	paths, err := filepath.Glob(filepath.Join(s.Dir, FeedbackDir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []Feedback
	for _, p := range paths {
		f, err := readJSON[Feedback](p)
		if err != nil {
			return nil, err
		}
		base := strings.TrimSuffix(filepath.Base(p), ".json")
		if key, _, ok := strings.Cut(base, ".closed-"); ok {
			f.Type, f.Key = TypeClose, key
		} else {
			f.Type, f.Key = TypeFeedback, base
		}
		out = append(out, f)
	}
	return out, nil
}

// feedbackIDs numbers feedback (#1, #2…) in the order it was given.
func feedbackIDs(recs []Feedback) map[string]int {
	var fs []Feedback
	for _, r := range recs {
		if r.Type == TypeFeedback {
			fs = append(fs, r)
		}
	}
	sort.SliceStable(fs, func(i, j int) bool {
		if !fs[i].At.Equal(fs[j].At) {
			return fs[i].At.Before(fs[j].At)
		}
		return fs[i].Key < fs[j].Key
	})
	out := map[string]int{}
	for i, f := range fs {
		out[f.Key] = i + 1
	}
	return out
}

// Add records a piece of feedback. Feedback with a SourceID already
// recorded is skipped (returned with ok=false).
func (s *Store) Add(f Feedback) (Feedback, bool, error) {
	if strings.TrimSpace(f.Text) == "" {
		return f, false, errors.New("feedback text is empty")
	}
	unlock, err := s.lock()
	if err != nil {
		return f, false, err
	}
	defer unlock()
	if err := s.migrate(true); err != nil {
		return f, false, err
	}
	recs, err := s.feedbackRecords()
	if err != nil {
		return f, false, err
	}
	ids := feedbackIDs(recs)
	for _, r := range recs {
		if r.Type == TypeFeedback && f.SourceID != "" && r.SourceID == f.SourceID {
			r.ID = ids[r.Key]
			return r, false, nil
		}
	}
	if f.At.IsZero() {
		f.At = time.Now().UTC()
	}
	f.Type, f.Key, f.ID = TypeFeedback, NewID(f.At), 0
	// Version is the number when recorded; reading prefers the hash's.
	if err := writeOnce(filepath.Join(s.Dir, FeedbackDir, f.Key+".json"), f); err != nil {
		return f, false, err
	}
	ids = feedbackIDs(append(recs, f))
	f.ID = ids[f.Key]
	return f, true, nil
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
			now := time.Now().UTC()
			return writeOnce(filepath.Join(s.Dir, FeedbackDir, it.Key+".closed-"+NewID(now)+".json"), Feedback{Type: TypeClose, At: now, Note: note})
		}
	}
	return fmt.Errorf("no feedback #%d", id)
}

// Items returns all feedback with its status, oldest first.
func (s *Store) Items() ([]Item, error) {
	if err := s.migrate(false); err != nil {
		return nil, err
	}
	recs, err := s.feedbackRecords()
	if err != nil {
		return nil, err
	}
	vs, err := s.versions(recs)
	if err != nil {
		return nil, err
	}
	number := map[string]int{}
	addressed := map[string]int{}
	for _, v := range vs {
		number[v.Hash] = v.Version
		for _, k := range v.AddressKeys {
			if _, ok := addressed[k]; !ok {
				addressed[k] = v.Version
			}
		}
	}
	closed := map[string]string{}
	for _, r := range recs {
		if r.Type == TypeClose {
			closed[r.Key] = r.Note
		}
	}
	ids := feedbackIDs(recs)
	var out []Item
	for _, r := range recs {
		if r.Type != TypeFeedback {
			continue
		}
		r.ID = ids[r.Key]
		if n, ok := number[r.Hash]; ok {
			r.Version = n
		}
		it := Item{Feedback: r, Status: "open"}
		if v, ok := addressed[r.Key]; ok {
			it.Status, it.AddressedIn = "addressed", v
		} else if note, ok := closed[r.Key]; ok {
			it.Status, it.CloseNote = "closed", note
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// --- older formats -------------------------------------------------------------

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

// legacyVersion is a line of versions.jsonl.
type legacyVersion struct {
	Version   int       `json:"version"`
	Hash      string    `json:"hash"`
	At        time.Time `json:"at"`
	Source    string    `json:"source"`
	Summary   string    `json:"summary,omitempty"`
	Addresses []int     `json:"addresses,omitempty"`
	Parts     []Part    `json:"parts"`
}

// migrate converts versions.jsonl and feedback.jsonl into record files.
// It's idempotent (each record has a fixed file name and content), so
// lines appended later by an older ship are picked up next time, and two
// machines converting the same lines write identical files.
func (s *Store) migrate(locked bool) error {
	lv, lf := filepath.Join(s.Dir, legacyVersions), filepath.Join(s.Dir, legacyFeedback)
	_, e1 := os.Stat(lv)
	_, e2 := os.Stat(lf)
	if e1 != nil && e2 != nil {
		return nil
	}
	if !locked {
		unlock, err := s.lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	recs, err := readLines[Feedback](lf)
	if err != nil {
		return err
	}
	keyOf := map[int]string{}
	for _, r := range recs {
		if r.Type != TypeFeedback && r.Type != "" {
			continue
		}
		key := r.At.UTC().Format("20060102T150405Z") + "-legacy" + strconv.Itoa(r.ID)
		keyOf[r.ID] = key
		r.Type, r.Key, r.ID = TypeFeedback, "", 0
		if err := writeOnce(filepath.Join(s.Dir, FeedbackDir, key+".json"), r); err != nil {
			return err
		}
	}
	for _, r := range recs {
		if r.Type != TypeClose {
			continue
		}
		if key, ok := keyOf[r.ID]; ok {
			c := Feedback{Type: TypeClose, At: r.At, Note: r.Note}
			if err := writeOnce(filepath.Join(s.Dir, FeedbackDir, key+".closed-legacy.json"), c); err != nil {
				return err
			}
		}
	}
	vs, err := readLines[legacyVersion](lv)
	if err != nil {
		return err
	}
	prev := ""
	for _, v := range vs {
		dir := filepath.Join(s.Dir, VersionsDir, Hash12(v.Hash))
		vf := versionFile{Hash: v.Hash}
		for _, p := range v.Parts {
			vf.Parts = append(vf.Parts, Part{Kind: p.Kind, Name: p.Name, Hash: p.Hash, Missing: p.Missing})
			// Keep a copy when the file is still as it was then.
			if !p.Missing && p.Path != "" && currentHash(p.Path) == p.Hash {
				dest := filepath.Join(dir, filesDir, filepath.FromSlash(CopyRel(p)))
				if _, err := os.Stat(dest); err != nil {
					_ = copyPart(p.Path, dest)
				}
			}
		}
		if err := writeOnce(filepath.Join(dir, versionFileName), vf); err != nil {
			return err
		}
		sf := seenFile{At: v.At, Source: v.Source, Summary: v.Summary, Parent: prev}
		for _, id := range v.Addresses {
			if k, ok := keyOf[id]; ok {
				sf.Addresses = append(sf.Addresses, k)
			}
		}
		if err := writeOnce(filepath.Join(dir, seenDir, "legacy.json"), sf); err != nil {
			return err
		}
		prev = v.Hash
	}
	return nil
}

// currentHash hashes a part's file or dir as the fingerprint does.
func currentHash(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if st.IsDir() {
		return hashDir(path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashBytes(b)
}
