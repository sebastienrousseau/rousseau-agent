package audit_egress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"
)

// ChainedSink wraps any [Sink] to stamp tamper-evident hash-chain
// metadata onto every emitted record. Landed for the
// FeatureAuditEgress compliance path (SOC 2, ISO 27001, HIPAA
// audit-trail immutability requirements) — the operator points
// the SIEM at a stream where each record cryptographically
// references its predecessor.
//
// # How it works
//
//	ChainedSink.Emit(rec):
//	    rec.Chain.Sequence = c.seq
//	    rec.Chain.PrevHash = c.prev
//	    rec.Chain.Hash     = SHA-256(canonicalBytes(rec))
//	    c.seq++
//	    c.prev = rec.Chain.Hash
//	    inner.Emit(rec)
//
// Where `canonicalBytes(rec)` is a deterministic byte
// representation of every user-populated field PLUS Sequence +
// PrevHash. See [canonicalHash].
//
// # Guarantees
//
//   - Any single-record mutation between emit and verify breaks
//     that record's Hash → verifier flags it.
//   - Insertion or reorder breaks the PrevHash chain of every
//     later record.
//   - Deletion leaves a sequence gap AND breaks the next record's
//     PrevHash.
//
// # Non-goals
//
//   - Cross-restart chaining is deferred. Each ChainedSink starts
//     a fresh chain at Sequence=0 with an empty PrevHash. SIEMs
//     record the sink identity (via a resource attribute) so
//     restarts show up as new chains rather than as breaks.
//   - Authentication of the ChainedSink itself. Anyone with
//     write access to the SIEM can replay a chain from scratch;
//     hash-chaining detects tampering INSIDE a chain, not chain
//     substitution. Solve with sink-side transport auth (OTLP
//     mTLS, HMAC headers) as usual.
//
// # Concurrency
//
// Every Emit takes the internal mutex so Sequence + PrevHash
// stay consistent under concurrent callers. The critical section
// is small (compute hash, update counters) so throughput is
// bounded by the wrapped sink's Emit, not the chain math.
//
// # Cross-restart continuity
//
// By default each ChainedSink starts a fresh chain at Sequence=0
// with an empty PrevHash. Pass [WithChainStore] to resume the
// chain across daemon restarts: the store loads
// (last_sequence, last_hash) at construction and receives
// (sequence, hash) after each Emit. A SIEM-side verifier then
// sees one continuous chain instead of one-chain-per-restart —
// crucial for the "did anything happen while the daemon was
// down?" compliance question.
type ChainedSink struct {
	inner  Sink
	store  ChainStore
	logger *slog.Logger

	mu   sync.Mutex
	seq  uint64
	prev string
}

// ChainStore persists the tail of the audit chain so a
// ChainedSink resumes from where the last daemon instance left
// off. Implementations MUST be safe for concurrent use — Save
// may be called from any Emit goroutine.
type ChainStore interface {
	// Load returns the last-written (sequence, hash). Returns
	// (0, "", nil) when the store has never seen a record —
	// callers treat that as "fresh chain".
	Load(ctx context.Context) (sequence uint64, hash string, err error)
	// Save records the most recent (sequence, hash). Called
	// once per emitted record, inside the ChainedSink's Emit
	// critical section, so state is atomically consistent with
	// what the wrapped sink saw.
	Save(ctx context.Context, sequence uint64, hash string) error
}

// ChainOption customises a [ChainedSink] built by [NewChainedSink].
type ChainOption func(*ChainedSink)

// WithChainStore enables cross-restart chain continuity. The
// store is queried once at construction to resume state, then
// written after every Emit. A nil store is treated as "no
// persistence" — matches the default zero-arg NewChainedSink
// call.
func WithChainStore(s ChainStore) ChainOption {
	return func(c *ChainedSink) { c.store = s }
}

// WithChainLogger installs a logger used for the (rare) store
// error paths. A nil logger falls back to [slog.Default]. Store
// errors are otherwise best-effort — the chain advances in
// memory regardless.
func WithChainLogger(l *slog.Logger) ChainOption {
	return func(c *ChainedSink) { c.logger = l }
}

// NewChainedSink wraps inner. A nil inner returns nil so misuse
// fails at the call site rather than at first Emit.
//
// When [WithChainStore] is passed, the store's most recent
// (sequence, hash) is loaded synchronously — a store-level
// error is logged but non-fatal (the chain resumes as a fresh
// one; the SIEM sees a chain break, which is more visible than
// a silent gap).
func NewChainedSink(inner Sink, opts ...ChainOption) *ChainedSink {
	if inner == nil {
		return nil
	}
	c := &ChainedSink{inner: inner}
	for _, opt := range opts {
		opt(c)
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.store != nil {
		seq, hash, err := c.store.Load(context.Background())
		if err != nil {
			c.logger.Warn("audit_egress.chain.resume_failed",
				slog.String("err", err.Error()),
				slog.String("hint", "chain will start fresh at sequence 0 — SIEM will see a chain break"),
			)
		} else if hash != "" {
			// Resume: next record uses (loaded_seq + 1, loaded_hash).
			c.seq = seq + 1
			c.prev = hash
			c.logger.Info("audit_egress.chain.resumed",
				slog.Uint64("next_sequence", c.seq),
				slog.String("prev_hash", hash[:min(16, len(hash))]),
			)
		}
	}
	return c
}

// Emit stamps chain metadata onto rec and delegates to the
// wrapped sink. If the wrapped sink returns an error the chain
// still advances — a record that failed to send is still a
// record that the operator's SIEM saw was ATTEMPTED, and the
// dropped-record counter on the wrapped sink is the authoritative
// "was this delivered" signal.
//
// When a [ChainStore] is configured, Save is called inside the
// critical section AFTER the in-memory state advances but
// BEFORE the wrapped sink Emit — so a store save that fails
// only loses that one record's continuity (not the whole
// batch), and a store save that succeeds guarantees the
// on-disk state matches what the SIEM will see.
func (c *ChainedSink) Emit(ctx context.Context, rec Record) error {
	c.mu.Lock()
	rec.Chain.Sequence = c.seq
	rec.Chain.PrevHash = c.prev
	rec.Chain.Hash = canonicalHash(rec)
	c.seq++
	c.prev = rec.Chain.Hash
	if c.store != nil {
		if err := c.store.Save(ctx, rec.Chain.Sequence, rec.Chain.Hash); err != nil {
			// Log but don't abort — audit is best-effort and a
			// wedged state DB shouldn't wedge the daemon.
			c.logger.Warn("audit_egress.chain.persist_failed",
				slog.Uint64("sequence", rec.Chain.Sequence),
				slog.String("err", err.Error()),
			)
		}
	}
	c.mu.Unlock()
	return c.inner.Emit(ctx, rec)
}

// Close delegates to the wrapped sink.
func (c *ChainedSink) Close(ctx context.Context) error {
	return c.inner.Close(ctx)
}

// Compile-time interface satisfaction.
var _ Sink = (*ChainedSink)(nil)

// VerifyChain walks records in order and returns nil when every
// record's Sequence, PrevHash, and Hash line up. On any mismatch
// returns a wrapped error with the offending index so a SIEM-side
// verifier can point at the exact record that broke the chain.
//
// Empty input is not an error — a zero-record chain is vacuously
// valid. Callers that require at least one record to pass
// verification should check `len(records) > 0` themselves.
func VerifyChain(records []Record) error {
	var (
		wantSeq  uint64
		wantPrev string
	)
	for i, rec := range records {
		if rec.Chain.Sequence != wantSeq {
			return fmt.Errorf("audit chain: sequence gap at index %d: got %d, want %d",
				i, rec.Chain.Sequence, wantSeq)
		}
		if rec.Chain.PrevHash != wantPrev {
			return fmt.Errorf("audit chain: prev-hash break at index %d: got %q, want %q",
				i, rec.Chain.PrevHash, wantPrev)
		}
		if rec.Chain.Hash != canonicalHash(rec) {
			return fmt.Errorf("audit chain: hash mismatch at index %d (record was mutated after emit)", i)
		}
		wantSeq = rec.Chain.Sequence + 1
		wantPrev = rec.Chain.Hash
	}
	return nil
}

// ErrChainBroken is returned in specific hardened contexts where
// the caller wants to check identity, not just presence.
var ErrChainBroken = errors.New("audit chain broken")

// canonicalHash produces a deterministic hex-encoded SHA-256 over
// the record's user-supplied fields plus the chain's Sequence +
// PrevHash. The field order MUST NEVER change once released —
// downstream verifiers pin to this byte layout.
//
// The layout:
//
//	uint64  Sequence            (big-endian, 8 bytes as ascii dec)
//	string  PrevHash            (raw bytes, then 0x00 separator)
//	int64   At.UnixNano         (ascii dec, then 0x00)
//	string  Category            (0x00-separated)
//	string  Actor
//	string  Verb
//	string  Object
//	string  Result
//	string  TraceID
//	string  Detail (sorted-key JSON of the map)
//
// The 0x00 separators keep concatenations unambiguous — no
// length-prefix needed. Detail is JSON-encoded with sorted keys
// so map iteration order (Go's random iteration) doesn't affect
// the hash.
func canonicalHash(r Record) string {
	h := sha256.New()
	write := func(b []byte) {
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0x00})
	}
	writeStr := func(s string) { write([]byte(s)) }

	writeStr(strconv.FormatUint(r.Chain.Sequence, 10))
	writeStr(r.Chain.PrevHash)
	writeStr(strconv.FormatInt(r.At.UTC().UnixNano(), 10))
	writeStr(r.Category)
	writeStr(r.Actor)
	writeStr(r.Verb)
	writeStr(r.Object)
	writeStr(r.Result)
	writeStr(r.TraceID)
	writeStr(canonicalDetail(r.Detail))
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalDetail serialises Detail with sorted keys so the
// output is deterministic regardless of Go's map iteration order.
// A nil / empty map returns the literal "{}" (matches json.Marshal
// of an empty map) so an "added key" versus "map became empty"
// change hashes differently from "map was always empty".
func canonicalDetail(d map[string]any) string {
	if len(d) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf []byte
	buf = append(buf, '{')
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			// k is a Go string, so json.Marshal here cannot
			// fail — the check exists to satisfy the linter and
			// document the invariant.
			kb = []byte(`""`)
		}
		buf = append(buf, kb...)
		buf = append(buf, ':')
		vb, err := json.Marshal(d[k])
		if err != nil {
			// json.Marshal only fails on cyclic / unsupported
			// types; audit callers control the map so this is a
			// pathological input. Fall back to the string form so
			// the hash is still reproducible.
			vb = []byte(fmt.Sprintf("%q", fmt.Sprint(d[k])))
		}
		buf = append(buf, vb...)
	}
	buf = append(buf, '}')
	return string(buf)
}

// timeForHash is a package-visible seam so tests can lock the
// time source used inside canonicalHash's UTC normalisation.
// Currently unused externally — reserved for future work that
// wants to compute hashes with a fixed clock.
var timeForHash = func(t time.Time) time.Time { return t.UTC() }

var _ = timeForHash // silence unused; keep hook available
