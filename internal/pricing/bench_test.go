package pricing_test

import (
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/pricing"
)

// BenchmarkEstimate_HotPath exercises the price-lookup + arithmetic on
// a realistic usage record. Runs on every recorded completion so it
// belongs in the hot path (per-request-per-completion frequency).
func BenchmarkEstimate_HotPath(b *testing.B) {
	usage := agent.Usage{
		InputTokens:              5000,
		OutputTokens:             800,
		CacheReadInputTokens:     12000,
		CacheCreationInputTokens: 340,
		CacheCreationEphemeral1h: 200,
		CacheCreationEphemeral5m: 140,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pricing.Estimate(usage, "claude-sonnet-4-6", nil)
	}
}

// BenchmarkEstimate_UnknownModel measures the "miss" path — the
// canonical-name normalisation loop plus the map miss.
func BenchmarkEstimate_UnknownModel(b *testing.B) {
	usage := agent.Usage{InputTokens: 1000}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pricing.Estimate(usage, "vendor-x/experimental-2026-01", nil)
	}
}
