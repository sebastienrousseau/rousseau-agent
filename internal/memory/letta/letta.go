// Package letta implements a Letta-style two-tier memory model as a
// forward path for [internal/recall]. The current recall provider
// treats memory as chunks + vector search — good for information
// retrieval, less good for the agent to *reason about its own
// state*.
//
// Letta (formerly MemGPT) splits memory into two tiers:
//
//   - **Core memory** — a small, always-in-context block the agent
//     itself can read and edit via tool calls
//     (`memory_write_core`). This is where identity-scoped facts
//     live: "user prefers concise replies", "working on rust project
//     X". Cost: fixed context-window budget per turn.
//   - **Archival memory** — a large, searchable store the agent
//     queries on demand (`memory_search_archival`). Today's implementation
//     is process-local + substring-ranked so it Just Works without a
//     vector-index dep; the SQLite/embedding backend is a follow-up
//     that plugs behind the same [Store] interface.
//
// The agent drives promotion/demotion between the two tiers — when
// core memory hits its byte budget on write, the oldest facts get
// demoted into archival automatically.
package letta

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultCoreBytes is the byte budget assigned to a session's core
// memory when the caller doesn't override it. Chosen to fit a
// meaningful set of ~10-20 short facts in typical model context
// windows while staying small enough that per-turn prompt overhead is
// negligible.
const DefaultCoreBytes = 2048

// CoreMemory is the small always-in-context block.
type CoreMemory struct {
	// SessionID scopes the memory. Cross-session sharing happens by
	// identity (see W2.2 identity resolver).
	SessionID string
	// Facts is the ordered list of memory entries.
	Facts []Fact
	// MaxBytes bounds the total serialised size of Facts. Zero uses
	// DefaultCoreBytes.
	MaxBytes int
	// UpdatedAt is the wall-clock time of the last mutation.
	UpdatedAt time.Time
}

// Fact is one entry in CoreMemory.
type Fact struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ByteSize returns the JSON-encoded size of the facts slice — used
// for MaxBytes accounting so the budget matches what the caller
// serialises into a prompt.
func (m CoreMemory) ByteSize() int {
	if len(m.Facts) == 0 {
		return 0
	}
	blob, err := json.Marshal(m.Facts)
	if err != nil {
		return 0
	}
	return len(blob)
}

// ArchivalEntry is one chunk in archival memory. Embedding stays as
// an escape hatch for a future vector-search backend — the in-memory
// implementation ignores it and ranks by substring hits.
type ArchivalEntry struct {
	SessionID string
	Text      string
	Embedding []float32
	CreatedAt time.Time
}

// Store is the persistence contract. Implementations must be safe
// for concurrent use.
type Store interface {
	// LoadCore returns the current core memory for sessionID.
	// Unknown session returns a zero-value CoreMemory with SessionID
	// populated and MaxBytes = DefaultCoreBytes.
	LoadCore(ctx context.Context, sessionID string) (CoreMemory, error)
	// WriteCore replaces the caller's CoreMemory in the store. When
	// the incoming Facts would exceed MaxBytes, the oldest are
	// demoted into archival memory until the payload fits.
	WriteCore(ctx context.Context, m CoreMemory) error
	// SearchArchival returns up to limit ArchivalEntries most
	// relevant to query.
	SearchArchival(ctx context.Context, sessionID, query string, limit int) ([]ArchivalEntry, error)
	// AppendArchival adds an entry to the archival tier.
	AppendArchival(ctx context.Context, e ArchivalEntry) error
	// DemoteOldest demotes the oldest N facts out of core memory
	// into archival. Called by WriteCore's budget enforcement and
	// exposed so callers can pre-emptively free space.
	DemoteOldest(ctx context.Context, sessionID string, n int) error
}

// ErrScaffold marks a Store constructor that hasn't landed yet.
// Reserved for [NewSQLiteStore] until the SQLite backend is wired.
var ErrScaffold = errors.New("memory/letta: SQLite backend not yet implemented (see docs/memory-letta.md)")

// NewSQLiteStore constructs the SQLite-backed [Store]. Returns
// ErrScaffold until the runtime is wired — use [NewMemoryStore] in
// the meantime for a fully functional process-local backend.
func NewSQLiteStore() (Store, error) {
	return nil, ErrScaffold
}

// NewMemoryStore returns a fully-functional Store that keeps
// everything in memory. Safe for concurrent use and appropriate for
// single-process daemons where memory persistence across restarts
// isn't required.
func NewMemoryStore() Store {
	return &memoryStore{
		core:     make(map[string]CoreMemory),
		archival: make(map[string][]ArchivalEntry),
	}
}

// memoryStore is the concurrent-safe in-memory implementation of Store.
type memoryStore struct {
	mu       sync.Mutex
	core     map[string]CoreMemory
	archival map[string][]ArchivalEntry
}

func (s *memoryStore) LoadCore(_ context.Context, sessionID string) (CoreMemory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.core[sessionID]; ok {
		return copyCore(m), nil
	}
	return CoreMemory{SessionID: sessionID, MaxBytes: DefaultCoreBytes}, nil
}

func (s *memoryStore) WriteCore(_ context.Context, m CoreMemory) error {
	if m.SessionID == "" {
		return errors.New("memory/letta: SessionID is required")
	}
	if m.MaxBytes <= 0 {
		m.MaxBytes = DefaultCoreBytes
	}
	m.UpdatedAt = time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	// Enforce budget by demoting oldest facts (by CreatedAt) until
	// the payload fits.
	for m.ByteSize() > m.MaxBytes && len(m.Facts) > 0 {
		sort.SliceStable(m.Facts, func(i, j int) bool {
			return m.Facts[i].CreatedAt.Before(m.Facts[j].CreatedAt)
		})
		oldest := m.Facts[0]
		m.Facts = m.Facts[1:]
		s.archival[m.SessionID] = append(s.archival[m.SessionID], ArchivalEntry{
			SessionID: m.SessionID,
			Text:      oldest.Key + ": " + oldest.Value,
			CreatedAt: time.Now().UTC(),
		})
	}
	s.core[m.SessionID] = copyCore(m)
	return nil
}

func (s *memoryStore) SearchArchival(_ context.Context, sessionID, query string, limit int) ([]ArchivalEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.archival[sessionID]
	type scored struct {
		e     ArchivalEntry
		score int
	}
	var hits []scored
	for _, e := range all {
		if score := substringScore(strings.ToLower(e.Text), q); score > 0 {
			hits = append(hits, scored{e: e, score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].e.CreatedAt.After(hits[j].e.CreatedAt)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]ArchivalEntry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out, nil
}

func (s *memoryStore) AppendArchival(_ context.Context, e ArchivalEntry) error {
	if e.SessionID == "" {
		return errors.New("memory/letta: SessionID is required")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archival[e.SessionID] = append(s.archival[e.SessionID], e)
	return nil
}

func (s *memoryStore) DemoteOldest(_ context.Context, sessionID string, n int) error {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.core[sessionID]
	if !ok || len(m.Facts) == 0 {
		return nil
	}
	sort.SliceStable(m.Facts, func(i, j int) bool {
		return m.Facts[i].CreatedAt.Before(m.Facts[j].CreatedAt)
	})
	if n > len(m.Facts) {
		n = len(m.Facts)
	}
	demoted := m.Facts[:n]
	m.Facts = m.Facts[n:]
	for _, f := range demoted {
		s.archival[sessionID] = append(s.archival[sessionID], ArchivalEntry{
			SessionID: sessionID,
			Text:      f.Key + ": " + f.Value,
			CreatedAt: time.Now().UTC(),
		})
	}
	m.UpdatedAt = time.Now().UTC()
	s.core[sessionID] = m
	return nil
}

// substringScore counts non-overlapping hits of every word in query
// against haystack. Zero means "no match, don't include."
func substringScore(haystack, query string) int {
	var score int
	for _, tok := range strings.Fields(query) {
		if len(tok) < 2 {
			continue
		}
		score += strings.Count(haystack, tok)
	}
	return score
}

// copyCore returns a deep copy of m so callers can't mutate the
// backing store by holding a returned CoreMemory.
func copyCore(m CoreMemory) CoreMemory {
	out := m
	if len(m.Facts) > 0 {
		out.Facts = append([]Fact(nil), m.Facts...)
	}
	return out
}
