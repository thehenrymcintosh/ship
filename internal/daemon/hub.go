package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/thehenrymcintosh/ship/internal/agent"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// Hub fans engine updates out to SSE subscribers. It implements
// engine.Publisher and never blocks the engine: slow subscribers drop.
type Hub struct {
	mu       sync.Mutex
	subs     map[*subscriber]struct{}
	pending  map[string]*store.RunSnapshot
	timer    *time.Timer
	inbox    map[string]bool
	debounce time.Duration
}

type subscriber struct {
	run string // "" = all runs (lite snapshots and inbox only)
	ch  chan []byte
}

// NewHub returns a hub. initialInbox seeds the inbox count.
func NewHub(initialInbox []string) *Hub {
	h := &Hub{subs: map[*subscriber]struct{}{}, pending: map[string]*store.RunSnapshot{}, inbox: map[string]bool{}, debounce: 100 * time.Millisecond}
	for _, id := range initialInbox {
		h.inbox[id] = true
	}
	return h
}

func (h *Hub) subscribe(run string) *subscriber {
	s := &subscriber{run: run, ch: make(chan []byte, 512)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *Hub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// matches reports whether a run-filtered subscriber wants events of id
// (the run itself or one of its children).
func (s *subscriber) matches(id string) bool {
	return s.run == id || strings.HasPrefix(id, s.run+".")
}

func frame(event string, data any) []byte {
	b, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, b))
}

func (h *Hub) send(s *subscriber, msg []byte) {
	select {
	case s.ch <- msg:
	default: // slow client: drop; the next snapshot catches it up
	}
}

// RunEvent records a snapshot change; flushes are debounced per run.
func (h *Hub) RunEvent(snap *store.RunSnapshot, e store.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending[snap.ID] = snap
	if h.timer == nil {
		h.timer = time.AfterFunc(h.debounce, h.flush)
	}
}

func (h *Hub) flush() {
	h.mu.Lock()
	pending := h.pending
	h.pending = map[string]*store.RunSnapshot{}
	h.timer = nil
	inboxChanged := false
	for id, s := range pending {
		in := s.Status.InInbox()
		if h.inbox[id] != in {
			inboxChanged = true
			if in {
				h.inbox[id] = true
			} else {
				delete(h.inbox, id)
			}
		}
	}
	count := len(h.inbox)
	subs := make([]*subscriber, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()

	for id, snap := range pending {
		lite := snap.Lite()
		liteMsg := frame("run", map[string]any{"id": id, "snapshot": lite})
		var fullMsg []byte
		for _, s := range subs {
			switch {
			case s.run == "":
				h.send(s, liteMsg)
			case s.matches(id):
				if fullMsg == nil {
					fullMsg = frame("run", map[string]any{"id": id, "snapshot": snap})
				}
				h.send(s, fullMsg)
			}
		}
	}
	if inboxChanged {
		msg := frame("inbox", map[string]int{"count": count})
		for _, s := range subs {
			h.send(s, msg)
		}
	}
}

// InboxCount returns the number of runs waiting for a human.
func (h *Hub) InboxCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.inbox)
}

// Output forwards a live output chunk to subscribers of the run.
func (h *Hub) Output(runID string, seq int, stream string, chunk []byte) {
	h.toRun(runID, frame("output", map[string]any{"run": runID, "seq": seq, "stream": stream, "chunk": string(chunk)}))
}

// Agent forwards a live agent event to subscribers of the run.
func (h *Hub) Agent(runID string, seq int, ev agent.UIEvent) {
	h.toRun(runID, frame("agent", map[string]any{"run": runID, "seq": seq, "kind": ev.Kind, "data": ev.Data}))
}

func (h *Hub) toRun(runID string, msg []byte) {
	h.mu.Lock()
	var subs []*subscriber
	for s := range h.subs {
		if s.run != "" && s.matches(runID) {
			subs = append(subs, s)
		}
	}
	h.mu.Unlock()
	for _, s := range subs {
		h.send(s, msg)
	}
}
