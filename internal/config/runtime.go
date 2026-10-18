package config

import (
	"time"
)

// RecallConfig configures the vector-based long-term memory (§9 of
// docs/IMPLEMENTATION_PLAN_2026_07_16.md). Zero-value disables recall
// entirely.
type RecallConfig struct {
	// Enabled toggles the entire subsystem.
	Enabled bool `mapstructure:"enabled"`
	// Embedder chooses the backend. Supported: "noop", "voyage",
	// "openai". "noop" produces zero-vectors — useful in tests + when
	// the operator is exercising storage without embedding cost.
	Embedder string `mapstructure:"embedder"`
	// EmbedderModel overrides the backend's default model.
	EmbedderModel string `mapstructure:"embedder_model"`
	// EmbedderAPIKey supplies credentials to the backend. Empty falls
	// back to the backend-specific env var (ROUSSEAU_VOYAGE_API_KEY
	// for voyage; ROUSSEAU_OPENAI_API_KEY then OPENAI_API_KEY for
	// openai).
	EmbedderAPIKey string `mapstructure:"embedder_api_key"`
	// EmbedderDims overrides auto-detected dimensionality; required
	// for non-well-known models.
	EmbedderDims int `mapstructure:"embedder_dims"`
	// ChunkTokens is the target chunk size for ingestion. Default 400.
	ChunkTokens int `mapstructure:"chunk_tokens"`
	// ChunkOverlap is the token overlap between adjacent chunks.
	// Default 40.
	ChunkOverlap int `mapstructure:"chunk_overlap"`
	// RetrievalK caps how many rows Recall returns per query.
	// Default 6.
	RetrievalK int `mapstructure:"retrieval_k"`
	// HybridWeight is the vector-vs-keyword blend passed to the
	// retriever. Default 0.7 (vector-heavy). Range [0, 1].
	HybridWeight float32 `mapstructure:"hybrid_weight"`
	// PurgeAfter drops rows older than this. Zero disables purge.
	// Format: any time.ParseDuration string (e.g. "4320h" for 180d).
	PurgeAfter string `mapstructure:"purge_after"`
}

// RateLimitConfig configures the per-JID token-bucket rate limiter.
// A zero-value config disables rate limiting entirely.
type RateLimitConfig struct {
	// Default is the fallback rate for transports without a specific
	// override. Format: "Nr/Duration", e.g. "10r/1m".
	Default string `mapstructure:"default"`
	// PerTransport overrides Default on a per-transport basis. Keys
	// are transport names ("whatsapp", "slack", "email", …).
	PerTransport map[string]string `mapstructure:"per_transport"`
	// MaxKeys caps the LRU of tracked senders. Zero uses the built-in
	// default (10 000).
	MaxKeys int `mapstructure:"max_keys"`
	// DeniedReply overrides the user-facing string returned when a
	// message is rate-limited. Empty uses ratelimit.DefaultDeniedReply.
	DeniedReply string `mapstructure:"denied_reply"`
}

// ResilienceConfig configures panic-recovery and circuit-breaker
// middleware.
type ResilienceConfig struct {
	// CircuitBreaker configures the provider-side breaker. The breaker
	// is wired only when max_failures is set; the other fields fall
	// back to the defaults documented on resilience.BreakerConfig.
	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker"`
	// Retry configures retries of retryable provider errors (rate
	// limits, overloaded, 5xx, request timeouts) for the API-key
	// providers. On by default with 3 attempts; max_attempts: 1
	// disables it. claudecli has its own retry logic and is not
	// wrapped.
	Retry RetryConfig `mapstructure:"retry"`
}

// RetryConfig mirrors resilience.RetryConfig via viper tags.
type RetryConfig struct {
	MaxAttempts     int `mapstructure:"max_attempts"`
	BaseDelayMS     int `mapstructure:"base_delay_ms"`
	MaxDelayMS      int `mapstructure:"max_delay_ms"`
	MaxRetryAfterMS int `mapstructure:"max_retry_after_ms"`
}

// CircuitBreakerConfig mirrors resilience.BreakerConfig via viper tags.
type CircuitBreakerConfig struct {
	MaxFailures uint32 `mapstructure:"max_failures"`
	IntervalMS  int    `mapstructure:"interval_ms"`
	TimeoutMS   int    `mapstructure:"timeout_ms"`
	HalfOpenMax uint32 `mapstructure:"half_open_max"`
}

// ObservabilityConfig configures the optional Prometheus metrics endpoint
// and OpenTelemetry tracer. Both are opt-in — leaving them empty means
// the daemon runs with zero inbound HTTP surface (matching rousseau's
// default posture) and zero telemetry output.
type ObservabilityConfig struct {
	// MetricsAddr binds the Prometheus /metrics endpoint, e.g. ":9100".
	// Empty disables the endpoint entirely.
	MetricsAddr string `mapstructure:"metrics_addr"`
	// OTLPEndpoint is the base URL of an OTLP-HTTP collector, e.g.
	// "http://otel-collector:4318". Empty leaves the tracer noop.
	OTLPEndpoint string `mapstructure:"otlp_endpoint"`
	// AuditEgress configures the enterprise-only streaming
	// audit-log sink. Zero value leaves the sink Nop (no records
	// leave the process) — the OSS stdout+SQLite audit path is
	// unaffected. Requires FeatureAuditEgress on the licence.
	AuditEgress AuditEgressConfig `mapstructure:"audit_egress"`
}

// AuditEgressConfig is the operator-facing view of
// internal/observability/audit_egress. Kept as a separate
// config type so mapstructure tags don't leak into the sink
// package.
type AuditEgressConfig struct {
	// Kind selects the sink implementation. Empty (default)
	// leaves audit egress disabled; "otlp_http" ships the
	// OTLP/HTTP logs pilot.
	Kind string `mapstructure:"kind"`
	// Endpoint is the OTLP/HTTP logs URL, e.g.
	// "https://siem.example.com/v1/logs". Required when
	// kind = "otlp_http".
	Endpoint string `mapstructure:"endpoint"`
	// Headers are added to every request. Standard use: bearer
	// tokens, Splunk HEC "Authorization: Splunk <token>".
	Headers map[string]string `mapstructure:"headers"`
	// BatchSize caps records per push. Zero uses the sink's
	// documented default.
	BatchSize int `mapstructure:"batch_size"`
	// FlushInterval bounds how long a partial batch waits
	// before being pushed. Zero uses the sink default.
	FlushInterval time.Duration `mapstructure:"flush_interval"`
	// QueueSize caps in-memory records; overflow drops oldest.
	// Zero uses the sink default.
	QueueSize int `mapstructure:"queue_size"`
	// HTTPTimeout caps a single push attempt. Zero uses the
	// sink default (10s).
	HTTPTimeout time.Duration `mapstructure:"http_timeout"`
	// Chained wraps the sink in a hash-chained tamper-evident
	// layer (see internal/observability/audit_egress.ChainedSink).
	// Compliance-officer visible feature; recommended for SOC 2 /
	// ISO 27001 / HIPAA audit-trail requirements.
	Chained bool `mapstructure:"chained"`
	// ChainHMACKeyFile, with Chained, adds a keyed MAC to every
	// record (rousseau.audit.chain.mac) so an edited and re-hashed
	// chain is detectable by a verifier holding the key. The file
	// holds at least 32 bytes and must be mode 0600 or 0400 (a podman
	// secret needs mode=0400); the file tools are denied the path. A
	// configured file that cannot be read, or is open to group or
	// others, stops startup.
	ChainHMACKeyFile string `mapstructure:"chain_hmac_key_file"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// StateConfig configures the session store.
//
// Two drivers ship in the core: sqlite (default — single-replica,
// zero-config, embedded) and postgres (multi-replica HA, requires
// an external server). Selection lives here rather than a top-
// level driver field so operators bind one struct and forget it.
type StateConfig struct {
	// Driver selects the backend: "sqlite" (default) or "postgres".
	// Empty is treated as "sqlite" so existing configs keep working
	// with no change.
	Driver string `mapstructure:"driver"`
	// Path is the SQLite database path. Ignored when driver is
	// "postgres".
	Path string `mapstructure:"path"`
	// DSN is the Postgres libpq-style URL, e.g.
	//   postgres://user:pass@host:5432/rousseau?sslmode=require
	// Ignored when driver is "sqlite". Required when driver is
	// "postgres".
	DSN string `mapstructure:"dsn"`
	// SessionTTL erases sessions not updated for this long (with
	// their per-session rows and claude transcripts), checked every
	// 6 hours. Zero (the default) keeps sessions until deleted.
	// SQLite only for now.
	SessionTTL time.Duration `mapstructure:"session_ttl"`
	// AutoMigrate upgrades an older SQLite store at startup (after
	// the same backup `rousseau migrate` takes) instead of exiting 78
	// and waiting for an operator. Ambiguous sender keys still stop
	// it. SQLite only: a Postgres upgrade needs an operator-taken
	// backup.
	AutoMigrate bool `mapstructure:"auto_migrate"`
}
