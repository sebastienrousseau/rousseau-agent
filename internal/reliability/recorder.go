package reliability

// Recorder is the thin abstraction the agent + approver + transport
// layers depend on to emit reliability samples. Every callsite that
// currently emits a log line for "turn happened", "tool denied",
// "handler failed" also calls Recorder.Record with a matching
// Sample.
//
// Two implementations ship:
//
//   - *Aggregator (this package): in-memory rolling ring, safe for
//     concurrent use, feeds the `rousseau reliability` CLI at
//     process-lifetime. Sufficient for daemon-mode inspection.
//   - state/sqlite.ReliabilitySampleStore: durable table, survives
//     restarts and makes samples visible across processes (a `rousseau
//     reliability` invocation from a shell reads from the same store
//     the whatsapp daemon writes to). Recommended for production.
//
// A MultiRecorder ships in this package so a daemon can Tee to both
// without every callsite knowing about both.
//
// Nil Recorder is safe: NopRecorder is the zero-value; every call
// through the interface no-ops. Callers therefore never have to
// nil-check.
type Recorder interface {
	Record(Sample)
}

// NopRecorder discards every sample. Used as the zero-value fallback
// so agent + transport callsites can dereference a Recorder without
// checking for nil.
type NopRecorder struct{}

// Record satisfies Recorder by dropping the sample on the floor.
func (NopRecorder) Record(Sample) {}

// Compile-time assertion: Aggregator already has Record(Sample), so
// it satisfies Recorder without a wrapper.
var _ Recorder = (*Aggregator)(nil)

// MultiRecorder fans a Sample out to every downstream recorder in
// order. Used by the daemon assembly to Tee samples to both the
// in-memory Aggregator (for the CLI's process-lifetime view) and
// the persistent SQLite store (for cross-restart survival).
//
// Ordering matters when a downstream Record panics: MultiRecorder
// runs recorders in slice order and does not recover — a panicking
// downstream aborts the whole chain, matching the fail-safe
// principle that reliability instrumentation must never silently
// mask a bug in one of its consumers.
type MultiRecorder struct {
	Recorders []Recorder
}

// Record satisfies Recorder by dispatching to each configured
// downstream in order. Nil entries are skipped (safe to construct
// the multi-recorder incrementally).
func (m *MultiRecorder) Record(s Sample) {
	if m == nil {
		return
	}
	for _, r := range m.Recorders {
		if r == nil {
			continue
		}
		r.Record(s)
	}
}

// NewMultiRecorder is the sugar constructor. Callers may also
// literal-construct the struct; the helper exists so daemon
// assembly reads left-to-right without a struct literal in the
// middle of a wiring block.
func NewMultiRecorder(recorders ...Recorder) *MultiRecorder {
	return &MultiRecorder{Recorders: recorders}
}
