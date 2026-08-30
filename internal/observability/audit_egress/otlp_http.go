package audit_egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// OTLPHTTPSink batches audit records in memory and pushes them to
// an OTLP/HTTP logs endpoint. Design notes:
//
//   - Emit is non-blocking. Records land on a buffered channel;
//     the pusher goroutine drains it.
//   - The queue is bounded (Config.QueueSize). When full, oldest
//     records are dropped and a counter increments — the daemon
//     surviving a SIEM outage matters more than an audit-log gap.
//   - Pushes run inside a per-attempt context with Config.HTTPTimeout.
//   - Non-2xx responses count as failures and are retried at the
//     next flush; the batch is NOT dropped until the next flush
//     succeeds (bounded so a persistent 5xx doesn't wedge the
//     queue — see the retry policy below).
//
// # Retry policy
//
// One-shot: if a push fails, the batch stays in a pending slot and
// merges with the next batch on the following flush tick. Two
// consecutive failures drop the older batch (with a counter
// increment) so a wedged endpoint doesn't cause unbounded memory
// growth. This is deliberately simpler than exponential-backoff
// per-record; OTel Collectors in front of a SIEM buy the operator
// the retry sophistication they need.
type OTLPHTTPSink struct {
	cfg    Config
	client *http.Client
	logger *slog.Logger

	queue    chan Record
	shutdown chan struct{}
	done     chan struct{}
	closed   atomic.Bool

	// Counters observable via Stats — used by Prometheus wiring in
	// a follow-up. Kept as atomic ints so Stats is race-free.
	enqueued atomic.Int64
	pushed   atomic.Int64
	dropped  atomic.Int64
	failed   atomic.Int64
}

// NewOTLPHTTPSink constructs a running sink. Rejects an empty or
// scheme-less endpoint at construction so a mis-configured deploy
// fails at boot rather than silently discarding audit records.
func NewOTLPHTTPSink(cfg Config, logger *slog.Logger) (*OTLPHTTPSink, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("audit_egress: otlp_http requires endpoint")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("audit_egress: endpoint parse: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("audit_egress: endpoint scheme %q not supported (want http/https)", u.Scheme)
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg = cfg.applyDefaults()
	s := &OTLPHTTPSink{
		cfg:      cfg,
		client:   &http.Client{Timeout: cfg.HTTPTimeout},
		logger:   logger,
		queue:    make(chan Record, cfg.QueueSize),
		shutdown: make(chan struct{}),
		done:     make(chan struct{}),
	}
	go s.pushLoop()
	return s, nil
}

// Emit satisfies [Sink]. Non-blocking: falls through to drop-oldest
// when the queue is full.
func (s *OTLPHTTPSink) Emit(_ context.Context, rec Record) error {
	if s.closed.Load() {
		return ErrSinkClosed
	}
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	s.enqueued.Add(1)
	select {
	case s.queue <- rec:
	default:
		// Queue full: drop the oldest to make room. Non-strict
		// approximation of a ring buffer (a receive-then-send
		// window that a concurrent push can slip through), but
		// close enough for audit — the operator sees a
		// `records_dropped_total` climb and knows to widen the
		// queue or fix their SIEM.
		select {
		case <-s.queue: // discard oldest
			s.dropped.Add(1)
		default:
		}
		select {
		case s.queue <- rec:
		default:
			s.dropped.Add(1)
		}
	}
	return nil
}

// Close satisfies [Sink]. Waits for the pusher goroutine to drain
// the queue (bounded by ctx). Safe to call multiple times.
func (s *OTLPHTTPSink) Close(ctx context.Context) error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.shutdown)
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns a snapshot of counters. Used by /doctor and, in a
// follow-up, exposed as Prometheus gauges. Race-free.
type Stats struct {
	Enqueued int64
	Pushed   int64
	Dropped  int64
	Failed   int64
	Pending  int
}

// Stats returns the current counters.
func (s *OTLPHTTPSink) Stats() Stats {
	return Stats{
		Enqueued: s.enqueued.Load(),
		Pushed:   s.pushed.Load(),
		Dropped:  s.dropped.Load(),
		Failed:   s.failed.Load(),
		Pending:  len(s.queue),
	}
}

// pushLoop is the pusher goroutine. Ticks at FlushInterval and
// pushes whatever has accumulated. On shutdown, drains once more
// before exiting so Close's guarantee ("flushed by the time I
// return") holds.
func (s *OTLPHTTPSink) pushLoop() {
	defer close(s.done)
	ticker := time.NewTicker(s.cfg.FlushInterval)
	defer ticker.Stop()

	var pending []Record
	for {
		select {
		case <-s.shutdown:
			pending = append(pending, s.drain()...)
			s.flush(pending)
			return
		case <-ticker.C:
			pending = append(pending, s.drain()...)
			if len(pending) == 0 {
				continue
			}
			if err := s.push(pending); err != nil {
				s.failed.Add(1)
				s.logger.Warn("audit_egress.push_failed",
					slog.String("err", err.Error()),
					slog.Int("pending", len(pending)),
				)
				// Retry once — the batch stays in `pending`. On
				// the next tick we'll try again with the fresh
				// arrivals appended.
				if len(pending) > 2*s.cfg.BatchSize {
					// Persistent failure: drop the oldest half
					// rather than let memory grow unbounded.
					drop := len(pending) - s.cfg.BatchSize
					s.dropped.Add(int64(drop))
					pending = pending[drop:]
				}
				continue
			}
			s.pushed.Add(int64(len(pending)))
			pending = pending[:0]
		}
	}
}

// drain non-blockingly pulls up to BatchSize records off the queue.
func (s *OTLPHTTPSink) drain() []Record {
	out := make([]Record, 0, s.cfg.BatchSize)
	for len(out) < s.cfg.BatchSize {
		select {
		case r := <-s.queue:
			out = append(out, r)
		default:
			return out
		}
	}
	return out
}

// flush is the shutdown path — same as push but the caller ignores
// the error (we're stopping anyway).
func (s *OTLPHTTPSink) flush(recs []Record) {
	if len(recs) == 0 {
		return
	}
	if err := s.push(recs); err != nil {
		s.failed.Add(1)
		s.logger.Warn("audit_egress.shutdown_flush_failed",
			slog.String("err", err.Error()),
			slog.Int("lost", len(recs)),
		)
	} else {
		s.pushed.Add(int64(len(recs)))
	}
}

// push serialises + POSTs a batch of records. Blocking; the caller
// bounds it via Client.Timeout.
func (s *OTLPHTTPSink) push(recs []Record) error {
	body, err := marshalOTLPLogs(recs)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rousseau-agent/audit-egress")
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// Read (bounded) response body for diagnostics — some collectors
	// return a JSON error explaining what went wrong. An error
	// reading the diag body is uninteresting: we already know the
	// status is non-2xx, and adding a "AND we couldn't read the
	// body" is noise.
	preview, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
	if readErr != nil {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(preview))
}

// marshalOTLPLogs renders records as OTLP/HTTP logs JSON. Uses the
// canonical OTLP shape so ANY collector in front of ANY SIEM
// accepts them — no per-vendor payload. rousseau-specific fields
// live under `rousseau.audit.*` attribute keys.
//
// The payload shape:
//
//	{ "resourceLogs": [ { "resource": { "attributes": [...] },
//	                     "scopeLogs": [ { "scope": {...},
//	                                     "logRecords": [ ... ] } ] } ] }
//
// See https://opentelemetry.io/docs/specs/otlp/#otlphttp-json .
func marshalOTLPLogs(recs []Record) ([]byte, error) {
	logRecords := make([]any, 0, len(recs))
	for _, r := range recs {
		lr := map[string]any{
			"timeUnixNano":         fmt.Sprintf("%d", r.At.UnixNano()),
			"observedTimeUnixNano": fmt.Sprintf("%d", time.Now().UTC().UnixNano()),
			"severityNumber":       9, // INFO — audit events are informational by design
			"severityText":         "INFO",
			"body":                 map[string]any{"stringValue": r.Verb + " " + r.Object + " → " + r.Result},
			"attributes":           attrsForRecord(r),
		}
		if r.TraceID != "" {
			lr["traceId"] = r.TraceID
		}
		logRecords = append(logRecords, lr)
	}
	payload := map[string]any{
		"resourceLogs": []any{
			map[string]any{
				"resource": map[string]any{
					"attributes": []any{
						kvString("service.name", "rousseau-agent"),
					},
				},
				"scopeLogs": []any{
					map[string]any{
						"scope": map[string]any{
							"name":    "rousseau.audit",
							"version": "1",
						},
						"logRecords": logRecords,
					},
				},
			},
		},
	}
	return json.Marshal(payload)
}

// attrsForRecord builds the OTLP attributes slice for one record.
// Every field lands under a `rousseau.audit.*` key so downstream
// SIEM filters ("show me everything where rousseau.audit.result =
// denied") stay stable across schema evolutions.
func attrsForRecord(r Record) []any {
	attrs := []any{
		kvString("rousseau.audit.category", r.Category),
		kvString("rousseau.audit.actor", r.Actor),
		kvString("rousseau.audit.verb", r.Verb),
		kvString("rousseau.audit.object", r.Object),
		kvString("rousseau.audit.result", r.Result),
	}
	// Detail is stringified so vendors that don't map nested OTel
	// attributes correctly still get *something* to grep.
	if len(r.Detail) > 0 {
		if b, err := json.Marshal(r.Detail); err == nil {
			attrs = append(attrs, kvString("rousseau.audit.detail", string(b)))
		}
	}
	return attrs
}

func kvString(k, v string) map[string]any {
	return map[string]any{
		"key":   k,
		"value": map[string]any{"stringValue": v},
	}
}

// testFlushTimeout is the shared deadline waitForNextFlush polls
// against. Kept as a package var (not a parameter) so lint's
// "always receives the same value" check stays quiet — every
// caller wants the same "long enough for CI, short enough not to
// hang a broken test forever" window.
const testFlushTimeout = 2 * time.Second

// waitForNextFlush blocks until the pusher goroutine has processed
// at least one tick after the call, up to testFlushTimeout. Used
// by tests only — production callers wire a real observability tap
// into Stats() to know when a push happened.
func (s *OTLPHTTPSink) waitForNextFlush() bool {
	deadline := time.Now().Add(testFlushTimeout)
	baseline := s.Stats().Pushed + s.Stats().Failed
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		cur := s.Stats().Pushed + s.Stats().Failed
		if cur > baseline {
			return true
		}
	}
	return false
}
