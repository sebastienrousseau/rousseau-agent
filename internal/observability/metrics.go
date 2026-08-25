// Package observability exposes rousseau-agent's metric surface and a
// small OpenTelemetry facade. It is intentionally opt-in: with no
// configuration the metric registry sits idle and the tracer is a
// noop, so importing this package costs nothing at runtime.
//
// # Metrics
//
// A per-process Prometheus registry is exposed on the address supplied
// via Config.MetricsAddr (e.g. ":9100"). The registry publishes the
// counters and histograms defined below plus the Go runtime + process
// collectors from client_golang. All rousseau metrics are prefixed
// with `rousseau_`.
//
// # Tracing
//
// [StartOTel] wires an OTLP-HTTP tracer provider against the endpoint
// supplied in Config.OTLPEndpoint (typically an OTel Collector on
// localhost:4318). When left blank the returned Shutdown function is
// a noop and every [StartSpan] returns a noop span; this keeps the
// call-site pattern the same whether or not tracing is enabled.
package observability

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the private Prometheus registry used by every rousseau
// metric. Callers should never mutate it directly; use the exported
// counters and histograms below.
var Registry = prometheus.NewRegistry()

var factory = promauto.With(Registry)

var (
	// ProviderLatency records the wall-clock latency of a single
	// provider round-trip, labelled by provider name and operation
	// (chat vs stream).
	ProviderLatency = factory.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rousseau_provider_latency_seconds",
		Help:    "Latency of one LLM provider round-trip, by provider and operation.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 10), // 50ms → 51s
	}, []string{"provider", "operation"})

	// ProviderErrors counts provider round-trips that returned an
	// error, labelled by provider and error category.
	ProviderErrors = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_provider_errors_total",
		Help: "Provider errors, by provider and category (rate_limit, auth, network, other).",
	}, []string{"provider", "category"})

	// TransportIncoming counts every message the router received and
	// dispatched into the agent loop, labelled by transport.
	TransportIncoming = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_transport_incoming_total",
		Help: "Inbound messages accepted from a transport and routed to the agent.",
	}, []string{"transport"})

	// TransportOutgoing counts every reply the router sent back on a
	// transport, labelled by transport and success outcome.
	TransportOutgoing = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_transport_outgoing_total",
		Help: "Outbound messages sent on a transport, by transport and status (ok, error).",
	}, []string{"transport", "status"})

	// CronFires counts every cron job invocation, labelled by job id
	// and outcome.
	CronFires = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_cron_fires_total",
		Help: "Cron job invocations, by job id and status (ok, error, skipped).",
	}, []string{"job", "status"})

	// ToolCalls counts every tool invocation attempt by the agent,
	// labelled by tool name and the approver's decision.
	ToolCalls = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_tool_calls_total",
		Help: "Tool call attempts, by tool name and approver decision (allow, deny).",
	}, []string{"tool", "decision"})

	// CompressorRewrites counts how many times the session compressor
	// rewrote a conversation, labelled by outcome.
	CompressorRewrites = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_compressor_rewrites_total",
		Help: "Session compression events, by outcome (rewrote, skipped, error).",
	}, []string{"outcome"})

	// SessionActive tracks the number of live sessions currently held
	// in memory by the router / TUI.
	SessionActive = factory.NewGauge(prometheus.GaugeOpts{
		Name: "rousseau_session_active",
		Help: "Number of chat sessions currently held in memory.",
	})

	// PanicsRecovered counts every panic the resilience middleware
	// caught before it could take down the daemon, labelled by the
	// surface that produced it (transport / tool / provider).
	PanicsRecovered = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_panics_recovered_total",
		Help: "Panics caught by the resilience recover middleware, by surface.",
	}, []string{"surface"})

	// CircuitState reports the current state of a circuit breaker
	// (0 = Closed, 1 = HalfOpen, 2 = Open), labelled by the wrapped
	// resource (provider name, transport name, tool name).
	CircuitState = factory.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rousseau_circuit_state",
		Help: "Circuit breaker state (0=Closed, 1=HalfOpen, 2=Open), by wrapped resource.",
	}, []string{"resource"})

	// CircuitTrips counts every transition into the Open state,
	// labelled by resource. Useful for alerts on repeated tripping.
	CircuitTrips = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_circuit_trips_total",
		Help: "Circuit breaker Open transitions, by wrapped resource.",
	}, []string{"resource"})

	// RateLimitDenied counts inbound messages the per-JID rate limiter
	// blocked, labelled by transport.
	RateLimitDenied = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_ratelimit_denied_total",
		Help: "Inbound messages denied by the per-JID rate limiter, by transport.",
	}, []string{"transport"})

	// SubagentSpawned counts every sub-agent Task Spawn dispatched,
	// labelled by the effective provider name.
	SubagentSpawned = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_subagent_spawned_total",
		Help: "Sub-agent Task invocations dispatched by Spawn, by provider.",
	}, []string{"provider"})

	// RouterDecisions counts routing decisions made by the
	// [router.Router] provider: which rule matched, which key was
	// selected, and which underlying provider executed. The default
	// fallback is reported with rule="default".
	RouterDecisions = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_router_decisions_total",
		Help: "Multi-model routing decisions, by matched rule name, chosen provider key, and underlying provider identifier.",
	}, []string{"rule", "chosen_key", "chosen_provider"})

	// PromptCacheTokens accumulates prompt-cache input tokens across
	// completions, split by type (read=hit, creation=miss) and by TTL
	// bucket (5m / 1h / unspecified). Operators can derive the cache
	// hit ratio as
	//
	//     rate(rousseau_prompt_cache_tokens_total{type="read"}[5m])
	//   / rate(rousseau_prompt_cache_tokens_total[5m])
	//
	// A hit ratio > 0.7 on a steady-state daemon means the system-
	// prompt cache breakpoint is set correctly and the cache is
	// paying for itself.
	PromptCacheTokens = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "rousseau_prompt_cache_tokens_total",
		Help: "Prompt-cache input tokens, by type (read=hit, creation=miss) and TTL bucket (5m|1h|unspecified).",
	}, []string{"provider", "model", "type", "ttl"})
)

func init() {
	// Include the standard Go runtime + process collectors so operators
	// don't need a second scrape target for basic host telemetry.
	Registry.MustRegister(collectors.NewGoCollector())
	Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// ObserveProviderLatency records the wall-clock time of a provider
// round-trip that started at start. Call as `defer` right after you
// begin the call.
func ObserveProviderLatency(provider, operation string, start time.Time) {
	ProviderLatency.WithLabelValues(provider, operation).Observe(time.Since(start).Seconds())
}

// ObservePromptCache emits [PromptCacheTokens] samples for one
// completion's usage counters. Provider adapters call this immediately
// after they successfully build an [agent.Response].
//
// The cache-creation number is split between the 1h and 5m TTL buckets
// when the provider populated the per-TTL fields; the residual (any
// creation tokens not attributed to either bucket) goes into the
// "unspecified" ttl label so no tokens are silently dropped.
func ObservePromptCache(provider, model string,
	read int, creation int,
	creation1h int, creation5m int,
) {
	if read > 0 {
		PromptCacheTokens.WithLabelValues(provider, model, "read", "unspecified").Add(float64(read))
	}
	if creation1h > 0 {
		PromptCacheTokens.WithLabelValues(provider, model, "creation", "1h").Add(float64(creation1h))
	}
	if creation5m > 0 {
		PromptCacheTokens.WithLabelValues(provider, model, "creation", "5m").Add(float64(creation5m))
	}
	// Residual: creation tokens the provider didn't attribute to
	// either TTL bucket. Zero on Anthropic when the response includes
	// a CacheCreation subobject; non-zero for older payloads.
	if residual := creation - creation1h - creation5m; residual > 0 {
		PromptCacheTokens.WithLabelValues(provider, model, "creation", "unspecified").Add(float64(residual))
	}
}

// StartMetricsServer runs an HTTP server exposing the metrics on
// /metrics until ctx is cancelled. addr accepts the standard
// `host:port` shape ("" disables the server entirely).
//
// The server is opt-in and refuses to bind an ambient port; operators
// must set it explicitly in config or via --metrics-addr. This keeps
// the daemon's inbound HTTP surface at zero by default.
func StartMetricsServer(ctx context.Context, addr string, logger *slog.Logger) error {
	if addr == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{
		Registry:            Registry,
		EnableOpenMetrics:   true,
		DisableCompression:  true,
		MaxRequestsInFlight: 8,
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok")) //nolint:errcheck // best-effort healthz write
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("metrics.starting", slog.String("addr", addr))
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) //nolint:errcheck // best-effort shutdown
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
