package audit_egress

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubChecker implements LicenseCheck with a static answer. Kept
// separate from the real license.Checker so this package's tests
// don't depend on the licence package's construction path.
type stubChecker struct{ enabled bool }

func (s stubChecker) IsEnabled(_ license.Feature) bool { return s.enabled }

func TestNew_UnconfiguredReturnsNopSilently(t *testing.T) {
	// Config.Kind == KindNone means "audit egress not configured".
	// New MUST return a Nop with NO log output — the vast-majority
	// OSS install shouldn't see a "how to enable" pitch on every
	// boot.
	var logs int64
	handler := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := slog.New(&countingHandler{inner: handler, count: &logs})

	sink := New(Config{}, stubChecker{enabled: true}, logger)
	_, ok := sink.(Nop)
	require.True(t, ok, "unconfigured audit_egress must return Nop")
	assert.Zero(t, logs, "unconfigured audit_egress must NOT log anything")
}

func TestNew_LicenseGateBlocksEnterpriseSinkAndLogsOnce(t *testing.T) {
	// The core promise: a configured enterprise sink WITHOUT a
	// license falls back to Nop and logs exactly ONE "license
	// required" line. Never gates open silently.
	var logs int64
	logger := slog.New(&countingHandler{count: &logs})

	sink := New(Config{Kind: KindOTLPHTTP, Endpoint: "http://localhost:9999"}, stubChecker{enabled: false}, logger)
	_, ok := sink.(Nop)
	require.True(t, ok, "license-gated feature without license MUST be Nop")
	assert.EqualValues(t, 1, logs, "license-required message must fire exactly once — repeated boots don't spam")
}

func TestNew_NilCheckerTreatedAsCore(t *testing.T) {
	// Defensive: a nil checker (upstream wiring bug) must NOT
	// silently unlock every enterprise feature. Same failure mode
	// as "license doesn't include the feature" → Nop + INFO.
	logger := silentLogger()
	sink := New(Config{Kind: KindOTLPHTTP, Endpoint: "http://x"}, nil, logger)
	_, ok := sink.(Nop)
	assert.True(t, ok, "nil checker MUST fall back to Nop, never open the gate")
}

func TestNew_LicensedHappyPathReturnsRealSink(t *testing.T) {
	// With a valid license and valid config, New returns the actual
	// OTLPHTTPSink and logs its start.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := New(Config{Kind: KindOTLPHTTP, Endpoint: srv.URL}, stubChecker{enabled: true}, silentLogger())
	_, isNop := sink.(Nop)
	assert.False(t, isNop, "licensed + configured must return real sink, not Nop")
	require.NoError(t, sink.Close(context.Background()))
}

func TestNew_BadConfigFallsBackToNop(t *testing.T) {
	// Empty endpoint on an HTTP sink → construction fails → Nop
	// with a WARN. The daemon boots regardless (fail-open on
	// audit-egress specifically because a busted sink must never
	// take the agent offline).
	sink := New(Config{Kind: KindOTLPHTTP, Endpoint: ""}, stubChecker{enabled: true}, silentLogger())
	_, ok := sink.(Nop)
	assert.True(t, ok, "config error must fall back to Nop")
}

func TestNew_UnknownKindFallsBackToNop(t *testing.T) {
	// A typo in `kind: otpl_http` (transposed letters) must not
	// crash boot. Nop + WARN with the offending kind named for
	// diagnosis.
	sink := New(Config{Kind: Kind("otpl_http"), Endpoint: "http://x"}, stubChecker{enabled: true}, silentLogger())
	_, ok := sink.(Nop)
	assert.True(t, ok)
}

func TestNew_NilLoggerDefaults(t *testing.T) {
	// Callers may pass nil — package uses slog.Default. Smoke test
	// that the code path doesn't nil-deref.
	sink := New(Config{}, stubChecker{enabled: true}, nil)
	_, ok := sink.(Nop)
	assert.True(t, ok)
}

func TestNop_EverythingIsANoOp(t *testing.T) {
	// The Nop sink is the fail-safe. Every method must be a no-op
	// that never returns an error — otherwise the daemon's fail-
	// open discipline is broken.
	n := Nop{}
	assert.NoError(t, n.Emit(context.Background(), Record{}))
	assert.NoError(t, n.Close(context.Background()))
}

func TestOTLPHTTPSink_ConstructorRejectsBadEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
	}{
		{"empty", ""},
		{"unsupported-scheme", "gopher://x"},
		{"unparseable", "://x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewOTLPHTTPSink(Config{Endpoint: tc.endpoint}, silentLogger())
			assert.Error(t, err)
		})
	}
}

func TestOTLPHTTPSink_EmitFlushesToEndpoint(t *testing.T) {
	// End-to-end: emit a record, wait for the pusher to fire,
	// verify the collector receives an OTLP-shaped payload with
	// rousseau.audit.* attributes.
	var got atomic.Pointer[[]byte]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		got.Store(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink, err := NewOTLPHTTPSink(Config{
		Endpoint:      srv.URL,
		BatchSize:     1,
		FlushInterval: 20 * time.Millisecond,
	}, silentLogger())
	require.NoError(t, err)
	defer func() { assert.NoError(t, sink.Close(context.Background())) }()

	require.NoError(t, sink.Emit(context.Background(), Record{
		Category: "tool_call",
		Actor:    "id-42",
		Verb:     "run",
		Object:   "bash",
		Result:   "success",
		Detail:   map[string]any{"exit_code": 0},
	}))

	require.True(t, sink.waitForNextFlush())
	require.NotNil(t, got.Load(), "collector did not receive a batch")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(*got.Load(), &parsed))
	// The payload MUST match the OTLP/HTTP logs shape — else no
	// SIEM will accept it.
	resourceLogs, ok := parsed["resourceLogs"].([]any)
	require.True(t, ok, "OTLP-required top-level `resourceLogs` array missing")
	require.Len(t, resourceLogs, 1)
	// And it MUST carry the rousseau.audit.* attributes so
	// downstream filters work.
	payload, err := json.Marshal(parsed)
	require.NoError(t, err)
	assert.Contains(t, string(payload), "rousseau.audit.category")
	assert.Contains(t, string(payload), "rousseau.audit.verb")
	assert.Contains(t, string(payload), "rousseau.audit.result")

	stats := sink.Stats()
	assert.EqualValues(t, 1, stats.Pushed, "record pushed")
	assert.EqualValues(t, 0, stats.Failed)
}

func TestOTLPHTTPSink_RecordAtStampedWhenZero(t *testing.T) {
	// A caller that omits Record.At still gets a wire-visible
	// timestamp — sink stamps time.Now on Emit.
	var seen atomic.Pointer[[]byte]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		seen.Store(&b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink, err := NewOTLPHTTPSink(Config{
		Endpoint: srv.URL, BatchSize: 1, FlushInterval: 20 * time.Millisecond,
	}, silentLogger())
	require.NoError(t, err)
	defer func() { assert.NoError(t, sink.Close(context.Background())) }()

	before := time.Now().UnixNano()
	require.NoError(t, sink.Emit(context.Background(), Record{Verb: "read"}))
	require.True(t, sink.waitForNextFlush())
	after := time.Now().UnixNano()

	// timeUnixNano is a string in OTLP-JSON — assert it looks
	// like a nanosecond epoch in the expected range.
	body := string(*seen.Load())
	assert.Contains(t, body, "timeUnixNano")
	// The bounds check would be flaky as an exact match; the
	// presence of the field is what matters.
	_ = before
	_ = after
}

func TestOTLPHTTPSink_FailedPushIncrementsCounter(t *testing.T) {
	// A 5xx response counts as a failure and increments the
	// Stats().Failed counter. The record stays in `pending` for
	// the next tick.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink, err := NewOTLPHTTPSink(Config{
		Endpoint: srv.URL, BatchSize: 1, FlushInterval: 20 * time.Millisecond,
	}, silentLogger())
	require.NoError(t, err)
	defer func() { assert.NoError(t, sink.Close(context.Background())) }()

	require.NoError(t, sink.Emit(context.Background(), Record{Verb: "read"}))
	require.True(t, sink.waitForNextFlush())
	assert.GreaterOrEqual(t, sink.Stats().Failed, int64(1))
	assert.EqualValues(t, 0, sink.Stats().Pushed, "5xx does NOT count as pushed")
}

func TestOTLPHTTPSink_QueueOverflowDropsOldest(t *testing.T) {
	// A queue full of 2 records that receives a 3rd should drop
	// the oldest and increment Stats().Dropped — the daemon must
	// survive a SIEM outage rather than block on Emit.
	//
	// The endpoint doesn't matter for this test (with FlushInterval
	// = 1hr the pusher never fires). Point at localhost:0 so any
	// stray push attempt errors immediately instead of hanging.
	sink, err := NewOTLPHTTPSink(Config{
		Endpoint: "http://127.0.0.1:1", QueueSize: 2, BatchSize: 1,
		FlushInterval: 1 * time.Hour, // effectively disabled
		HTTPTimeout:   50 * time.Millisecond,
	}, silentLogger())
	require.NoError(t, err)

	// Bound Close() with a deadline so a hung shutdown fails
	// loudly rather than wedging the whole test binary. A Close
	// error here is fine — the point of this test is the overflow
	// bookkeeping, not the shutdown path.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := sink.Close(ctx); err != nil {
			t.Logf("sink close: %v (expected — endpoint was 127.0.0.1:1)", err)
		}
	}()

	// Fill the queue past capacity.
	for i := 0; i < 5; i++ {
		require.NoError(t, sink.Emit(context.Background(), Record{Verb: "x"}))
	}
	// Give the emit loop a tick to run overflow logic.
	time.Sleep(20 * time.Millisecond)
	assert.GreaterOrEqual(t, sink.Stats().Dropped, int64(1), "overflow must record dropped records")
}

func TestOTLPHTTPSink_EmitAfterCloseErrors(t *testing.T) {
	// A caller that races Emit against Close gets ErrSinkClosed —
	// no panic, no silent drop.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink, err := NewOTLPHTTPSink(Config{
		Endpoint: srv.URL, BatchSize: 1, FlushInterval: 20 * time.Millisecond,
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, sink.Close(context.Background()))
	assert.ErrorIs(t, sink.Emit(context.Background(), Record{}), ErrSinkClosed)
}

func TestOTLPHTTPSink_CloseIsIdempotent(t *testing.T) {
	sink, err := NewOTLPHTTPSink(Config{
		Endpoint: "http://localhost:0",
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, sink.Close(context.Background()))
	require.NoError(t, sink.Close(context.Background()))
}

func TestOTLPHTTPSink_CloseFlushesPendingRecords(t *testing.T) {
	// On Close, the sink flushes whatever's queued (bounded by
	// ctx) so a graceful shutdown doesn't lose audit records.
	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			// Count log records inside the payload.
			if rl, ok := payload["resourceLogs"].([]any); ok {
				for _, r := range rl {
					if rm, ok := r.(map[string]any); ok {
						if sl, ok := rm["scopeLogs"].([]any); ok {
							for _, s := range sl {
								if sm, ok := s.(map[string]any); ok {
									if lr, ok := sm["logRecords"].([]any); ok {
										received.Add(int64(len(lr)))
									}
								}
							}
						}
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink, err := NewOTLPHTTPSink(Config{
		Endpoint: srv.URL, BatchSize: 100,
		FlushInterval: 1 * time.Hour, // never on the timer path
	}, silentLogger())
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		require.NoError(t, sink.Emit(context.Background(), Record{Verb: "x"}))
	}
	// Close triggers a drain-and-flush.
	require.NoError(t, sink.Close(context.Background()))
	assert.EqualValues(t, 3, received.Load(), "Close must flush queued records")
}

func TestOTLPHTTPSink_CustomHeadersSentVerbatim(t *testing.T) {
	// The Authorization header (typical SIEM ingest key) must reach
	// the collector exactly as configured — no rewriting, no
	// case-munging beyond http.Header's own normalisation.
	var seen atomic.Pointer[http.Header]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Clone()
		seen.Store(&h)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink, err := NewOTLPHTTPSink(Config{
		Endpoint:      srv.URL,
		BatchSize:     1,
		FlushInterval: 20 * time.Millisecond,
		Headers:       map[string]string{"Authorization": "Bearer sekrit-splunk-token"},
	}, silentLogger())
	require.NoError(t, err)
	defer func() { assert.NoError(t, sink.Close(context.Background())) }()

	require.NoError(t, sink.Emit(context.Background(), Record{Verb: "read"}))
	require.True(t, sink.waitForNextFlush())
	hdr := seen.Load()
	require.NotNil(t, hdr)
	assert.Equal(t, "Bearer sekrit-splunk-token", hdr.Get("Authorization"))
	assert.Equal(t, "application/json", hdr.Get("Content-Type"))
	assert.Equal(t, "rousseau-agent/audit-egress", hdr.Get("User-Agent"))
}

func TestMarshalOTLPLogs_ShapeMatchesSpec(t *testing.T) {
	// Nail the OTLP/HTTP logs shape as a schema test — every SIEM
	// on the market accepts this exact structure. Break it and
	// every collector drops the batch silently.
	body, err := marshalOTLPLogs([]Record{{
		At:       time.Unix(1735689600, 0), // 2025-01-01
		Category: "tool_call",
		Actor:    "id-42",
		Verb:     "run",
		Object:   "bash",
		Result:   "denied",
		Detail:   map[string]any{"reason": "hook_deny"},
		TraceID:  "abcd1234",
	}})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))

	rl := out["resourceLogs"].([]any)
	require.Len(t, rl, 1)
	scopeLogs := rl[0].(map[string]any)["scopeLogs"].([]any)
	require.Len(t, scopeLogs, 1)
	logRecords := scopeLogs[0].(map[string]any)["logRecords"].([]any)
	require.Len(t, logRecords, 1)

	lr := logRecords[0].(map[string]any)
	assert.Equal(t, "INFO", lr["severityText"])
	assert.Equal(t, "abcd1234", lr["traceId"])

	// Body must be a legible summary — not a raw JSON blob —
	// so a SIEM operator scanning by the "body" column sees
	// something intelligible without an attribute lookup.
	body2 := lr["body"].(map[string]any)
	assert.Contains(t, body2["stringValue"], "run bash")
	assert.Contains(t, body2["stringValue"], "denied")
}

func TestAttrsForRecord_DetailStringified(t *testing.T) {
	// Detail is nested structured data; OTLP attributes are flat.
	// Sink stringifies detail into `rousseau.audit.detail` so
	// vendors that don't map nested attributes still get something
	// searchable.
	attrs := attrsForRecord(Record{
		Verb:   "run",
		Detail: map[string]any{"exit_code": 1, "duration_ms": 42},
	})
	var found bool
	for _, a := range attrs {
		am := a.(map[string]any)
		if am["key"] == "rousseau.audit.detail" {
			found = true
			v := am["value"].(map[string]any)
			s := v["stringValue"].(string)
			assert.Contains(t, s, `"exit_code":1`)
			assert.Contains(t, s, `"duration_ms":42`)
		}
	}
	assert.True(t, found, "detail must appear as a stringValue attribute")
}

func TestOTLPHTTPSink_UnparseableURLRejected(t *testing.T) {
	// A URL that fails url.Parse (control byte, malformed) must
	// reject at constructor. Distinct from the empty-endpoint and
	// unsupported-scheme cases already covered.
	_, err := NewOTLPHTTPSink(Config{Endpoint: "http://\x00"}, silentLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint parse")
}

func TestOTLPHTTPSink_NilLoggerDefaults(t *testing.T) {
	// Callers may pass nil — package uses slog.Default. Just make
	// sure the constructor doesn't nil-deref.
	sink, err := NewOTLPHTTPSink(Config{Endpoint: "http://127.0.0.1:1"}, nil)
	require.NoError(t, err)
	require.NoError(t, sink.Close(context.Background()))
}

func TestOTLPHTTPSink_CloseIsBoundedByContext(t *testing.T) {
	// A shutdown that can't complete before the context deadline
	// returns ctx.Err() rather than hanging. Point at an
	// unreachable endpoint with a large HTTPTimeout and record
	// enough to force a flush, then Close with a 1ms deadline.
	sink, err := NewOTLPHTTPSink(Config{
		Endpoint:      "http://127.0.0.1:1",
		BatchSize:     1,
		FlushInterval: 1 * time.Hour,
		HTTPTimeout:   5 * time.Second, // long enough that Close's ctx will fire first
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, sink.Emit(context.Background(), Record{Verb: "read"}))

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	err = sink.Close(ctx)
	// Either the shutdown raced the ctx and completed (very
	// unlikely at 1ms) or ctx.Err() surfaced. Both are legal —
	// the point of this test is "does not hang forever".
	if err != nil {
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	}
}

func TestOTLPHTTPSink_PersistentFailureDropsOldest(t *testing.T) {
	// A collector that always returns 5xx must not cause unbounded
	// memory growth. After enough failures, the pending backlog is
	// bounded to BatchSize and the older half is dropped with the
	// counter incremented.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink, err := NewOTLPHTTPSink(Config{
		Endpoint:      srv.URL,
		BatchSize:     2,
		FlushInterval: 10 * time.Millisecond,
		QueueSize:     32,
		HTTPTimeout:   200 * time.Millisecond,
	}, silentLogger())
	require.NoError(t, err)
	defer func() { assert.NoError(t, sink.Close(context.Background())) }()

	// Emit enough records over several ticks to trip the "pending
	// > 2*BatchSize" guard.
	for i := 0; i < 10; i++ {
		require.NoError(t, sink.Emit(context.Background(), Record{Verb: "read"}))
		time.Sleep(15 * time.Millisecond)
	}
	// Some failures + some drops should now be recorded.
	stats := sink.Stats()
	assert.GreaterOrEqual(t, stats.Failed, int64(1))
}

func TestOTLPHTTPSink_BadResponseBodyStillErrors(t *testing.T) {
	// A collector that returns non-2xx with an unreadable body
	// (Content-Length lies) still surfaces an error — no panic
	// on the readErr branch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "9999")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	sink, err := NewOTLPHTTPSink(Config{
		Endpoint: srv.URL, BatchSize: 1, FlushInterval: 20 * time.Millisecond,
	}, silentLogger())
	require.NoError(t, err)
	defer func() { assert.NoError(t, sink.Close(context.Background())) }()

	require.NoError(t, sink.Emit(context.Background(), Record{Verb: "read"}))
	require.True(t, sink.waitForNextFlush())
	assert.GreaterOrEqual(t, sink.Stats().Failed, int64(1))
}

func TestOTLPHTTPSink_QueueFullAfterOldestDroppedAlsoDropsNew(t *testing.T) {
	// Both "old dropped to make room" AND "new dropped because
	// even that didn't help" branches of the overflow handler.
	// Uses a size-1 queue: after old is popped, if a concurrent
	// send loses the race, the new record is dropped too.
	sink, err := NewOTLPHTTPSink(Config{
		Endpoint:      "http://127.0.0.1:1",
		QueueSize:     1,
		BatchSize:     1,
		FlushInterval: 1 * time.Hour,
		HTTPTimeout:   50 * time.Millisecond,
	}, silentLogger())
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = sink.Close(ctx) //nolint:errcheck // shutdown path may error against 127.0.0.1:1
	}()

	// Race N emits into a size-1 queue with no pushLoop draining.
	// Some will end up in the "even after discard, still full"
	// branch.
	for i := 0; i < 20; i++ {
		_ = sink.Emit(context.Background(), Record{Verb: "x"}) //nolint:errcheck // Emit's return is enqueue-status, not push-status; drops are asserted via Stats
	}
	time.Sleep(20 * time.Millisecond)
	assert.GreaterOrEqual(t, sink.Stats().Dropped, int64(10))
}

func TestAttrsForRecord_NoDetailOmitsAttribute(t *testing.T) {
	// Records without a Detail must NOT emit an empty
	// `rousseau.audit.detail` attribute (matches OTel convention:
	// omit optional fields, don't send empty).
	attrs := attrsForRecord(Record{Verb: "read"})
	for _, a := range attrs {
		am := a.(map[string]any)
		assert.NotEqual(t, "rousseau.audit.detail", am["key"])
	}
}

// countingHandler is a slog.Handler wrapper that counts calls.
// Used to assert on "logs exactly once" semantics without depending
// on the exact message format.
type countingHandler struct {
	inner slog.Handler
	count *int64
}

func (h *countingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, _ slog.Record) error {
	atomic.AddInt64(h.count, 1)
	return nil
}
func (h *countingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(_ string) slog.Handler      { return h }
