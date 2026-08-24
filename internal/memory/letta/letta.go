// Package letta scaffolds a Letta-style two-tier memory model as a
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
//   - **Archival memory** — a large, vector-searchable store the
//     agent queries on demand (`memory_search_archival`). This is
//     the chunk+embedding path that today's recall already ships.
//
// The agent drives promotion/demotion between the two tiers via
// tool calls — the same way ChatGPT's Memory feature works.
//
// # Status
//
// Scaffold only. The types + interface are in place; the SQLite
// backend, the four memory-tools, and the system-prompt integration
// land as W4.1 in the roadmap.
package letta

import (
	"context"
	"errors"
	"time"
)

// CoreMemory is the small always-in-context block. Kept in memory
// (with a SQLite mirror for persistence) so reads are zero-latency.
type CoreMemory struct {
	// SessionID scopes the memory. Cross-session sharing happens by
	// identity (see W2.2 identity resolver).
	SessionID string
	// Facts is the ordered list of memory entries.
	Facts []Fact
	// MaxBytes bounds the total serialised size of Facts. When an
	// insertion would exceed the budget, the oldest facts are
	// demoted to archival (via [Store.DemoteOldest]).
	MaxBytes int
	// UpdatedAt is the wall-clock time of the last mutation.
	UpdatedAt time.Time
}

// Fact is one entry in CoreMemory.
type Fact struct {
	Key       string
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ArchivalEntry is one chunk in archival memory. Mirrors the shape
// of [internal/recall]'s Chunk so the archival tier can be a thin
// wrapper over the existing recall store initially.
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
	// Empty session returns a zero-value CoreMemory with a default
	// MaxBytes budget (typically 2 KiB).
	LoadCore(ctx context.Context, sessionID string) (CoreMemory, error)
	// WriteCore replaces the caller's CoreMemory in the store.
	WriteCore(ctx context.Context, m CoreMemory) error
	// SearchArchival returns up to limit ArchivalEntries most
	// similar to query (semantic search over embeddings).
	SearchArchival(ctx context.Context, sessionID, query string, limit int) ([]ArchivalEntry, error)
	// AppendArchival adds an entry to the archival tier.
	AppendArchival(ctx context.Context, e ArchivalEntry) error
	// DemoteOldest promotes the oldest N facts out of core memory
	// into archival memory. Called by [CoreMemory.MaxBytes] enforcement.
	DemoteOldest(ctx context.Context, sessionID string, n int) error
}

// ErrScaffold is returned by every constructor in this package
// while the runtime is being built. Enables downstream code to
// build against the interfaces without a functional backend.
var ErrScaffold = errors.New("memory/letta: runtime not yet implemented (see docs/memory-letta.md)")

// NewSQLiteStore constructs the intended SQLite-backed [Store].
// Returns ErrScaffold until W4.1 lands.
func NewSQLiteStore() (Store, error) {
	return nil, ErrScaffold
}
