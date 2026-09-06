package reliability

import (
	"github.com/prometheus/client_golang/prometheus"
)

// PrometheusRecorder is a [Recorder] that turns each incoming
// Sample into a Prometheus observation on the shared
// observability registry. Metrics are shaped per the Phase-2.3
// naming convention documented in docs/reliability.md so
// existing scrapers (Grafana, Alertmanager) pick them up
// automatically.
//
// This is the second half of the Prometheus surface:
//
//   Aggregator      → in-memory rolling summary → `rousseau reliability`
//   SQLite store    → durable sample table       → cross-process reads
//   PrometheusRecorder → observability registry  → /metrics scrape
//
// All three share the same Sample stream. The MultiRecorder in
// the daemon assembly fans one sample out to all three at zero
// coordination cost.
//
// Registration is idempotent per Prometheus registry — the same
// PrometheusRecorder can be constructed multiple times against
// the same registry without error. Reusing observability.Registry
// (not a fresh one) is what makes samples show up on the running
// daemon's /metrics endpoint.
type PrometheusRecorder struct {
	turns          *prometheus.CounterVec
	requestSeconds prometheus.Histogram
	requestTokens  prometheus.Histogram
	requestCalls   prometheus.Histogram
	confidence     *prometheus.HistogramVec
	violations     *prometheus.CounterVec
	faultsTotal    *prometheus.CounterVec
	upstreamFaults *prometheus.CounterVec
}

// NewPrometheusRecorder builds the recorder and registers every
// metric with reg. Pass observability.Registry to hook the
// running daemon's /metrics endpoint.
//
// Panics on registration failure (a duplicate registration on a
// shared registry) — this is a boot-time programmer error, not a
// runtime condition. Callers can build a private registry for
// isolated tests.
func NewPrometheusRecorder(reg prometheus.Registerer) *PrometheusRecorder {
	factory := promauto(reg)
	return &PrometheusRecorder{
		turns: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "rousseau_agent_requests_total",
			Help: "Total agent turns completed, by outcome. Denominator for the Safety compliance rate.",
		}, []string{"outcome"}),
		requestSeconds: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "rousseau_agent_request_duration_seconds",
			Help:    "Wall-clock duration of a single agent turn. Consistency C_res feeds off the CV of this series bucketed by session.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 12), // 50ms → ~3.5 min
		}),
		requestTokens: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "rousseau_agent_request_tokens",
			Help:    "Input + output tokens per agent turn. Consistency C_res second resource dimension.",
			Buckets: prometheus.ExponentialBuckets(50, 2, 14), // 50 → ~400k
		}),
		requestCalls: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "rousseau_agent_request_tool_calls",
			Help:    "Tool calls per agent turn. Consistency C_res third resource dimension.",
			Buckets: prometheus.LinearBuckets(0, 1, 20), // 0..19
		}),
		confidence: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rousseau_agent_confidence_score",
			Help:    "Agent-reported confidence for the completed turn, labelled by observed outcome. Predictability metrics derive from this in PromQL.",
			Buckets: prometheus.LinearBuckets(0, 0.05, 21), // 0..1 in 0.05 steps
		}, []string{"outcome"}),
		violations: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "rousseau_agent_safety_violations_total",
			Help: "Approver denials + policy violations. Safety S_comp derives from this. Never alert on a rolled-up gauge — alert on this counter directly per arXiv:2602.16666 §3.4.",
		}, []string{"constraint", "severity"}),
		faultsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "rousseau_agent_fault_turns_total",
			Help: "Agent turns stratified by (outcome, upstream-fault-observed). Robustness R_fault is Acc_faulted / Acc_clean over this series.",
		}, []string{"outcome", "fault"}),
		upstreamFaults: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "rousseau_agent_upstream_faults_total",
			Help: "Upstream-fault events (network timeout, connection refused, subprocess exit). Diagnostic for correlating R_fault dips with concrete provider outages.",
		}, []string{"kind"}),
	}
}

// Record satisfies [Recorder]. Dispatches each Sample to the
// matching Prometheus surface based on Dimension + SubMetric.
// Unknown dimensions are silently dropped — the Prometheus
// registry doesn't need to know every future sub-metric a
// bespoke extension might add.
func (p *PrometheusRecorder) Record(s Sample) {
	if p == nil {
		return
	}
	switch s.Dimension {
	case DimSafety:
		switch s.SubMetric {
		case "turn":
			outcome := "success"
			if s.Value == 0 {
				outcome = "failure"
			}
			p.turns.WithLabelValues(outcome).Inc()
		case "violation":
			constraint := labelOrUnknown(s.Metadata, "constraint")
			severity := labelOrUnknown(s.Metadata, "severity")
			p.violations.WithLabelValues(constraint, severity).Inc()
		}
	case DimConsistency:
		switch s.SubMetric {
		case "resource_cv_latency":
			// Sample.Value is ms; the histogram is in seconds
			// per Prometheus/OTel convention.
			p.requestSeconds.Observe(s.Value / 1000)
		case "resource_cv_tokens":
			p.requestTokens.Observe(s.Value)
		case "resource_cv_calls":
			p.requestCalls.Observe(s.Value)
		}
	case DimPredictability:
		if s.SubMetric == "pair" {
			outcome := "failure"
			if s.Metadata["outcome"] == "1" {
				outcome = "success"
			}
			p.confidence.WithLabelValues(outcome).Observe(s.Value)
		}
	case DimRobustness:
		if s.SubMetric == "fault" {
			outcome := "success"
			if s.Value == 0 {
				outcome = "failure"
			}
			fault := labelOrUnknown(s.Metadata, "fault")
			p.faultsTotal.WithLabelValues(outcome, fault).Inc()
			// Emit the correlator counter when a fault WAS
			// observed so operators can graph "what class of
			// upstream fault caused the R_fault dip."
			if fault == "true" {
				kind := labelOrUnknown(s.Metadata, "fault_kind")
				p.upstreamFaults.WithLabelValues(kind).Inc()
			}
		}
	}
}

// labelOrUnknown fetches a Metadata value, defaulting to
// "unknown" so a missing tag never leaks into an empty
// Prometheus label (which would produce an ugly `{constraint=""}`
// series). Prometheus best practice is to always have a
// non-empty label value.
func labelOrUnknown(m map[string]string, key string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return "unknown"
}

// promauto is a local alias for promauto.With so callers don't
// have to import promauto themselves. Kept as a thin helper for
// readability at the constructor call sites.
func promauto(reg prometheus.Registerer) promautoFactory {
	return promautoFactory{reg: reg}
}

// promautoFactory is a tiny shim mirroring promauto.Factory's
// surface: NewCounterVec / NewHistogram / NewHistogramVec that
// register-and-return in one step. Kept private so future
// switching to the upstream promauto.Factory package is a
// one-line change (import + factory := promauto.With(reg)).
type promautoFactory struct {
	reg prometheus.Registerer
}

// register tolerates prometheus.AlreadyRegisteredError so the
// recorder can be reconstructed against a shared registry (the
// process-wide observability.Registry) any number of times —
// each daemon-assembly call in a test suite, each re-init on
// SIGHUP — without panicking. Non-AlreadyRegistered errors
// still panic (a genuine programmer bug at boot).
func register[T prometheus.Collector](reg prometheus.Registerer, c T) T {
	if err := reg.Register(c); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			// Reuse the existing series so scraped values keep
			// accumulating rather than resetting on re-init.
			if existing, ok := are.ExistingCollector.(T); ok {
				return existing
			}
		}
		panic(err)
	}
	return c
}

func (f promautoFactory) NewCounterVec(opts prometheus.CounterOpts, labels []string) *prometheus.CounterVec {
	return register(f.reg, prometheus.NewCounterVec(opts, labels))
}

func (f promautoFactory) NewHistogram(opts prometheus.HistogramOpts) prometheus.Histogram {
	return register(f.reg, prometheus.NewHistogram(opts))
}

func (f promautoFactory) NewHistogramVec(opts prometheus.HistogramOpts, labels []string) *prometheus.HistogramVec {
	return register(f.reg, prometheus.NewHistogramVec(opts, labels))
}
