package observability

import "github.com/prometheus/client_golang/prometheus"

// ProviderRetries counts retried provider round-trips, by provider.
// A steady non-zero rate means the upstream is rate-limiting or
// flapping; a sudden burst usually precedes a breaker trip.
var ProviderRetries = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "rousseau_provider_retries_total",
	Help: "Provider round-trips retried after a retryable error, by provider.",
}, []string{"provider"})
