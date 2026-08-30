// Package audit_egress implements the enterprise-only audit-log
// egress surface — the first real gate wired against the license
// seam from `internal/license`.
//
// # Boundary
//
// See [`docs/COMMERCIAL.md`](../../../docs/COMMERCIAL.md) §2.2. Base
// observability (stdout slog, SQLite session history, Prometheus
// scrape, OpenTelemetry tracer, baseline redact rules) stays in the
// OSS core. What this package ships — streaming audit-log push to
// external SIEMs — is gated on `license.FeatureAuditEgress`.
//
// # Fail-open discipline
//
// Every failure mode in this package is treated as a diagnostic,
// never as fatal:
//
//   - No configuration → [Nop] sink, silent.
//   - License doesn't unlock the feature → [Nop] sink, ONE INFO log
//     line naming the licence-required path.
//   - Bad configuration (unknown kind, missing endpoint) → [Nop]
//     sink, ONE WARN log line, daemon boots.
//   - Runtime push failure (network, 5xx) → in-memory retry with
//     bounded backoff, drop-oldest overflow policy, structured log.
//     Never blocks the caller's goroutine.
//
// The daemon MUST remain operational when audit egress is broken —
// customers whose SIEM ingest breaks should still see agent replies
// arrive; the audit gap is a fix-later problem, not a stop-work
// one.
//
// # Wire format
//
// The [OTLPHTTPSink] emits standard OTLP/HTTP logs
// (application/json). The record shape [Record] maps 1:1 to
// OTel `LogRecord`s with rousseau-specific attribute keys prefixed
// `rousseau.audit.*` so downstream SIEMs can filter cleanly.
package audit_egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

// Kind identifies a sink implementation. String values are wire-
// facing (they appear in operator config); never rename without a
// compat shim.
type Kind string

// Kind constants — one per supported sink.
const (
	// KindNone is the shipped default. No configuration → no sink →
	// no external audit push. Every core telemetry path is untouched.
	KindNone Kind = ""
	// KindOTLPHTTP posts OTLP/HTTP logs (application/json) to the
	// configured endpoint. The most portable option — every major
	// SIEM (Splunk, Datadog, New Relic, Grafana Loki, Honeycomb, OTel
	// Collector) accepts OTLP/HTTP.
	KindOTLPHTTP Kind = "otlp_http"
)

// Config configures the audit-egress subsystem. The zero value
// disables egress entirely — the daemon boots normally with a Nop
// sink installed.
//
// Every knob has a safe default: an operator who sets `kind:
// otlp_http` + `endpoint: https://…` gets working egress without
// tuning batching / retry / auth headers.
type Config struct {
	// Kind selects the sink implementation.
	Kind Kind `mapstructure:"kind"`
	// Endpoint is the destination URL. Required for HTTP-based
	// sinks. Any scheme validation lives in the sink constructor.
	Endpoint string `mapstructure:"endpoint"`
	// Headers are extra HTTP headers on every push (typically an
	// Authorization: Bearer <token> line pointing at the SIEM's
	// ingest key). Values are sent verbatim — treat them as
	// secrets in configuration.
	Headers map[string]string `mapstructure:"headers"`
	// BatchSize caps records-per-push. Zero uses [DefaultBatchSize].
	BatchSize int `mapstructure:"batch_size"`
	// FlushInterval bounds staleness — a partial batch flushes on
	// this cadence. Zero uses [DefaultFlushInterval].
	FlushInterval time.Duration `mapstructure:"flush_interval"`
	// QueueSize caps the in-memory backlog. When full, oldest
	// records are dropped and a counter increments. Zero uses
	// [DefaultQueueSize].
	QueueSize int `mapstructure:"queue_size"`
	// HTTPTimeout bounds a single push. Zero uses
	// [DefaultHTTPTimeout].
	HTTPTimeout time.Duration `mapstructure:"http_timeout"`
}

// Defaults chosen for a "sensible SIEM ingest under normal load"
// profile — 512 records per push, one push every 5 seconds, 4096-
// record backlog, 10s per-request timeout. These match what
// operators tune to on OTel Collector deployments the day after
// they install one.
const (
	// DefaultBatchSize is the record count per push.
	DefaultBatchSize = 512
	// DefaultFlushInterval is the max staleness for a partial batch.
	DefaultFlushInterval = 5 * time.Second
	// DefaultQueueSize is the in-memory backlog cap.
	DefaultQueueSize = 4096
	// DefaultHTTPTimeout bounds one push attempt.
	DefaultHTTPTimeout = 10 * time.Second
)

// Record is one audit-log entry, sink-agnostic. Maps 1:1 to an OTel
// LogRecord — [OTLPHTTPSink] does the translation.
//
// The field set is intentionally narrow. Compliance regimes ask two
// questions of every audit event: (a) who did what to what and (b)
// when. Everything else lives in [Detail] as free-form structured
// data.
type Record struct {
	// At is the observation time. Sink implementations stamp this
	// from time.Now if the caller leaves it zero, so callers can
	// omit it in the common case.
	At time.Time
	// Category groups related events for SIEM dashboards. Common
	// values: "tool_call", "auth", "config_change", "license".
	Category string
	// Actor is the opaque identity of whatever took the action —
	// typically the resolved cross-transport identity ID
	// (`identity.ID`). Never an email or display name.
	Actor string
	// Verb is the action taken: "read", "write", "run", "approve",
	// "deny", "start", "stop", …
	Verb string
	// Object is what the action targeted — a file path, a tool
	// name, a session ID, a transport JID. Wire-facing so downstream
	// SIEMs can dedupe / filter; caller is responsible for redacting
	// anything sensitive before it lands here (the redact package
	// still runs on the emitted log line for defence in depth).
	Object string
	// Result is the outcome: "success", "denied", "error". Kept as
	// a string rather than an enum so future outcomes don't require
	// a schema migration.
	Result string
	// Detail is free-form structured data — tool inputs, hook
	// verdict reasons, error details. Sinks that serialize to JSON
	// place this under a `rousseau.audit.detail` attribute; sinks
	// with a flatter schema may drop it (never silently — the
	// caller-visible telemetry path is stdout slog, which always
	// preserves the full record).
	Detail map[string]any
	// TraceID pins to the OTel span this event was observed inside
	// (when tracing is on). Downstream SIEM correlation joins on
	// this.
	TraceID string
}

// Sink is the egress surface every enterprise-only backend
// satisfies. Emit must not block — sinks internally queue and push
// asynchronously.
type Sink interface {
	// Emit hands rec to the sink for eventual delivery. Returns an
	// error only for "the record was rejected" (e.g. schema
	// violation, sink closed). Transport-level failures are handled
	// asynchronously and surfaced via observability counters, not
	// this return value.
	Emit(ctx context.Context, rec Record) error
	// Close drains any in-flight batch (bounded by ctx) and
	// releases sink resources. Safe to call multiple times.
	Close(ctx context.Context) error
}

// Nop is the shipped default: every Emit is a no-op. Used when
// audit egress isn't configured or the licence doesn't unlock it.
type Nop struct{}

// Emit satisfies [Sink].
func (Nop) Emit(context.Context, Record) error { return nil }

// Close satisfies [Sink].
func (Nop) Close(context.Context) error { return nil }

// LicenseCheck is the narrow slice of [license.Checker] this package
// depends on. Extracted so tests can substitute without pulling the
// full license package's construction path.
type LicenseCheck interface {
	IsEnabled(feature license.Feature) bool
}

// New builds a Sink from cfg + checker. Returns [Nop] in the three
// documented no-op cases — no config, licence doesn't unlock, bad
// config — with the reason surfaced through logger. The daemon
// boots regardless; that's the fail-open discipline documented on
// the package.
//
// Nil logger uses slog.Default.
func New(cfg Config, checker LicenseCheck, logger *slog.Logger) Sink {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Kind == KindNone {
		return Nop{}
	}
	if checker == nil || !checker.IsEnabled(license.FeatureAuditEgress) {
		logger.Info("audit_egress.license_required",
			slog.String("kind", string(cfg.Kind)),
			slog.String("feature", string(license.FeatureAuditEgress)),
			slog.String("how_to_enable", "set ROUSSEAU_LICENSE_KEY to a Team or Enterprise licence — see docs/COMMERCIAL.md"),
		)
		return Nop{}
	}
	sink, err := build(cfg, logger)
	if err != nil {
		logger.Warn("audit_egress.config_failed",
			slog.String("kind", string(cfg.Kind)),
			slog.String("err", err.Error()),
			slog.String("effect", "audit egress is DISABLED — daemon is booting on the core observability path"),
		)
		return Nop{}
	}
	logger.Info("audit_egress.started",
		slog.String("kind", string(cfg.Kind)),
		slog.String("endpoint", cfg.Endpoint),
	)
	return sink
}

// build dispatches on Kind. Kept separate from New so the licence-
// check + logging shell stays trivially testable.
func build(cfg Config, logger *slog.Logger) (Sink, error) {
	switch cfg.Kind {
	case KindOTLPHTTP:
		return NewOTLPHTTPSink(cfg, logger)
	default:
		return nil, fmt.Errorf("unknown sink kind %q", cfg.Kind)
	}
}

// applyDefaults folds every zero-value field to its documented
// default. Called by every sink constructor so operators can supply
// only the fields they care about.
func (c Config) applyDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = DefaultFlushInterval
	}
	if c.QueueSize <= 0 {
		c.QueueSize = DefaultQueueSize
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = DefaultHTTPTimeout
	}
	return c
}

// ErrSinkClosed is returned by Emit after Close.
var ErrSinkClosed = errors.New("audit_egress: sink is closed")
