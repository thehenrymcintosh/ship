package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// File names inside a run dir.
const (
	EventsFile   = "events.jsonl"
	SnapshotFile = "run.json"
	BriefFile    = "brief.md"
	PipelineDir  = "pipeline"
	VisitsDir    = "visits"
	LockFile     = ".lock"
)

// Store is the state root (~/.ship/state).
type Store struct {
	Root string
}

// New returns a store rooted at root, creating it (mode 0700).
func New(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "runs"), 0o700); err != nil {
		return nil, err
	}
	return &Store{Root: root}, nil
}

// RunsDir is state/runs.
func (s *Store) RunsDir() string { return filepath.Join(s.Root, "runs") }

// RunDir is state/runs/<id>.
func (s *Store) RunDir(id string) string { return filepath.Join(s.RunsDir(), id) }

// IDs lists every run id, sorted.
func (s *Store) IDs() ([]string, error) {
	entries, err := os.ReadDir(s.RunsDir())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(s.RunsDir(), e.Name(), EventsFile)); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// ErrNotFound is returned when no run matches.
var ErrNotFound = errors.New("run not found")

// AmbiguousError lists several matching runs.
type AmbiguousError struct{ Matches []string }

func (e *AmbiguousError) Error() string {
	return "several runs match: " + strings.Join(e.Matches, ", ")
}

// Resolve finds the run whose id equals q, or the unique run containing q.
func (s *Store) Resolve(q string) (string, error) {
	ids, err := s.IDs()
	if err != nil {
		return "", err
	}
	var matches []string
	for _, id := range ids {
		if id == q {
			return id, nil
		}
		if strings.Contains(id, q) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: %q", ErrNotFound, q)
	case 1:
		return matches[0], nil
	}
	return "", &AmbiguousError{Matches: matches}
}

// Load reads a run's snapshot, rebuilding it from events when run.json is
// missing or stale.
func (s *Store) Load(id string) (*RunSnapshot, error) {
	return LoadDir(s.RunDir(id))
}

// LoadDir reads the snapshot of a run dir.
func LoadDir(dir string) (*RunSnapshot, error) {
	snap, err := readSnapshot(dir)
	if err == nil {
		// Cheap freshness check: compare with the log's last seq.
		if last, lerr := lastSeq(dir); lerr == nil && last == snap.LastEventSeq {
			return snap, nil
		}
	}
	snap, _, err = Rebuild(dir)
	return snap, err
}

// List loads every run snapshot.
func (s *Store) List() ([]*RunSnapshot, error) {
	ids, err := s.IDs()
	if err != nil {
		return nil, err
	}
	var out []*RunSnapshot
	for _, id := range ids {
		snap, err := s.Load(id)
		if err != nil {
			slog.Warn("skipping unreadable run", "id", id, "err", err)
			continue
		}
		out = append(out, snap)
	}
	return out, nil
}

func readSnapshot(dir string) (*RunSnapshot, error) {
	b, err := os.ReadFile(filepath.Join(dir, SnapshotFile))
	if err != nil {
		return nil, err
	}
	var s RunSnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func lastSeq(dir string) (int, error) {
	evs, _, err := ReadEvents(dir)
	if err != nil {
		return 0, err
	}
	if len(evs) == 0 {
		return 0, nil
	}
	return evs[len(evs)-1].Seq, nil
}

// ReadEvents reads a run's events. A truncated trailing line is dropped; the
// returned offset is the length of the well-formed prefix.
func ReadEvents(dir string) ([]Event, int64, error) {
	f, err := os.Open(filepath.Join(dir, EventsFile))
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	var out []Event
	var good int64
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var e Event
			if jerr := json.Unmarshal(bytes.TrimSpace(line), &e); jerr != nil {
				if err == io.EOF || isLastLine(r) {
					slog.Warn("dropping corrupt trailing event line", "dir", dir)
					return out, good, nil
				}
				return nil, 0, fmt.Errorf("%s: corrupt event after seq %d: %w", dir, len(out), jerr)
			}
			out = append(out, e)
			good += int64(len(line))
		} else if len(line) > 0 {
			slog.Warn("dropping truncated trailing event line", "dir", dir, "bytes", len(line))
			return out, good, nil
		}
		if err == io.EOF {
			return out, good, nil
		}
		if err != nil {
			return nil, 0, err
		}
	}
}

func isLastLine(r *bufio.Reader) bool {
	_, err := r.Peek(1)
	return err != nil
}

// Rebuild replays a run's events into a fresh snapshot.
func Rebuild(dir string) (*RunSnapshot, []Event, error) {
	evs, _, err := ReadEvents(dir)
	if err != nil {
		return nil, nil, err
	}
	if len(evs) == 0 {
		return nil, nil, fmt.Errorf("%s: no events", dir)
	}
	s := &RunSnapshot{}
	for _, e := range evs {
		if err := Apply(s, e); err != nil {
			return nil, nil, err
		}
	}
	return s, evs, nil
}

// RunLog is an open, writable run: its event log and in-memory snapshot.
// Only the process holding the run (the daemon, or a foreground run) writes.
type RunLog struct {
	mu       sync.Mutex
	dir      string
	f        *os.File
	snap     RunSnapshot
	snapJSON []byte
	// OnEvent is called after each event is applied, outside the lock.
	OnEvent func(e Event, snap *RunSnapshot)
	now     func() time.Time
}

// Dir returns the run dir.
func (r *RunLog) Dir() string { return r.dir }

// Create creates a run dir and an empty log.
func (s *Store) Create(id string) (*RunLog, error) {
	dir := s.RunDir(id)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("run %s already exists", id)
	}
	if err := os.MkdirAll(filepath.Join(dir, VisitsDir), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, PipelineDir), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, EventsFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &RunLog{dir: dir, f: f, now: time.Now}, nil
}

// Open opens an existing run for writing, replaying its events. A truncated
// trailing line is cut off so new events append cleanly.
func (s *Store) Open(id string) (*RunLog, error) {
	dir := s.RunDir(id)
	evs, good, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, EventsFile)
	if st, err := os.Stat(path); err == nil && st.Size() != good {
		if err := os.Truncate(path, good); err != nil {
			return nil, err
		}
	}
	r := &RunLog{dir: dir, now: time.Now}
	for _, e := range evs {
		if err := Apply(&r.snap, e); err != nil {
			return nil, err
		}
	}
	if err := r.writeSnapshot(); err != nil {
		return nil, err
	}
	r.f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Emit appends one event (write, fsync), applies it, and rewrites run.json.
// No state may change except through Emit.
func (r *RunLog) Emit(typ, actor string, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	r.mu.Lock()
	e := Event{Seq: r.snap.LastEventSeq + 1, TS: r.now().UTC(), Type: typ, Actor: actor, Data: raw}
	line, err := json.Marshal(e)
	if err != nil {
		r.mu.Unlock()
		return Event{}, err
	}
	line = append(line, '\n')
	if _, err := r.f.Write(line); err != nil {
		r.mu.Unlock()
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	if err := fsync(r.f); err != nil {
		r.mu.Unlock()
		return Event{}, fmt.Errorf("fsync events: %w", err)
	}
	if err := Apply(&r.snap, e); err != nil {
		r.mu.Unlock()
		return Event{}, err
	}
	if err := r.writeSnapshot(); err != nil {
		r.mu.Unlock()
		return Event{}, err
	}
	cb := r.OnEvent
	var snap *RunSnapshot
	if cb != nil {
		snap = r.copyLocked()
	}
	r.mu.Unlock()
	if cb != nil {
		cb(e, snap)
	}
	return e, nil
}

// Snapshot returns a deep copy of the current snapshot.
func (r *RunLog) Snapshot() *RunSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.copyLocked()
}

func (r *RunLog) copyLocked() *RunSnapshot {
	var s RunSnapshot
	if err := json.Unmarshal(r.snapJSON, &s); err != nil {
		panic(err)
	}
	return &s
}

func (r *RunLog) writeSnapshot() error {
	b, err := json.MarshalIndent(&r.snap, "", "  ")
	if err != nil {
		return err
	}
	r.snapJSON = b
	return WriteFileAtomic(filepath.Join(r.dir, SnapshotFile), b, 0o600)
}

// Close closes the log file.
func (r *RunLog) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// WriteFileAtomic writes via a temp file, fsync and rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := fsync(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Lock is a held flock.
type Lock struct{ f *os.File }

// ErrLocked is returned when another process holds the lock.
var ErrLocked = errors.New("lock is held by another process")

// TryLock takes an exclusive non-blocking flock on path.
func TryLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// IsLocked reports whether another process holds the flock on path.
func IsLocked(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	l, err := TryLock(path)
	if err != nil {
		return errors.Is(err, ErrLocked)
	}
	l.Unlock()
	return false
}

// Unlock releases the lock.
func (l *Lock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	l.f.Close()
	l.f = nil
}

// fsync flushes f to the device. os.File.Sync uses F_FULLFSYNC on macOS,
// which flushes the whole drive cache and costs ~10ms per event; plain
// fsync(2) gives the same guarantee Linux does.
func fsync(f *os.File) error {
	for {
		err := unix.Fsync(int(f.Fd()))
		if err != unix.EINTR {
			return err
		}
	}
}
