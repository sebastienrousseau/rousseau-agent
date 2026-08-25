package pricing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/pricing"
)

func TestEstimate_KnownModelSumsAllDimensions(t *testing.T) {
	// claude-sonnet-4-6: $3.00 input / $15.00 output / $6.00 1h-write
	// / $3.75 5m-write / $0.30 read (all per 1M tokens).
	usage := agent.Usage{
		InputTokens:              1_000_000, // $3.00
		OutputTokens:             500_000,   // $7.50
		CacheReadInputTokens:     2_000_000, // $0.60
		CacheCreationInputTokens: 300_000,   // (split below)
		CacheCreationEphemeral1h: 200_000,   // $1.20
		CacheCreationEphemeral5m: 100_000,   // $0.375
	}
	cost, ok := pricing.Estimate(usage, "claude-sonnet-4-6", nil)
	assert.True(t, ok)
	assert.InDelta(t, 3.00+7.50+0.60+1.20+0.375, cost, 0.001)
}

func TestEstimate_UnknownModelReturnsFalse(t *testing.T) {
	_, ok := pricing.Estimate(agent.Usage{InputTokens: 100}, "no-such-model", nil)
	assert.False(t, ok)
}

func TestEstimate_ResidualCacheCreationBilledAt5mRate(t *testing.T) {
	// Total creation = 1000, per-TTL split = 0. Residual 1000 must
	// bill at the 5m rate (the API default when the caller doesn't
	// specify a TTL).
	usage := agent.Usage{
		CacheCreationInputTokens: 1_000_000,
	}
	cost, ok := pricing.Estimate(usage, "claude-sonnet-4-6", nil)
	assert.True(t, ok)
	assert.InDelta(t, 3.75, cost, 0.001) // 5m write rate for sonnet
}

func TestEstimate_ZeroUsageReturnsZero(t *testing.T) {
	cost, ok := pricing.Estimate(agent.Usage{}, "claude-sonnet-4-6", nil)
	assert.True(t, ok)
	assert.Equal(t, 0.0, cost)
}

func TestEstimate_HandlesModelWithVersionSuffix(t *testing.T) {
	usage := agent.Usage{InputTokens: 1_000_000}
	// Various common suffixes should still resolve.
	tests := []string{
		"claude-opus-4-6",
		"claude-opus-4-6@20260615",
		"claude-opus-4-6:latest",
		"anthropic/claude-opus-4-6",
	}
	for _, m := range tests {
		cost, ok := pricing.Estimate(usage, m, nil)
		assert.Truef(t, ok, "unresolved model %q", m)
		assert.InDeltaf(t, 15.00, cost, 0.001, "wrong cost for %q", m)
	}
}

func TestEstimate_CustomTableOverridesDefault(t *testing.T) {
	custom := pricing.Table{
		"my-model": {InputPerMTok: 42.00, OutputPerMTok: 100.00},
	}
	cost, ok := pricing.Estimate(agent.Usage{
		InputTokens:  500_000,
		OutputTokens: 1_000_000,
	}, "my-model", custom)
	assert.True(t, ok)
	assert.InDelta(t, 21.00+100.00, cost, 0.001)
}
