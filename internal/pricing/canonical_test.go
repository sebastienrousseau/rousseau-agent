package pricing

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestCanonical_DotSeparatedVersionSuffix covers deployments that pin a
// dated build with a dot ("claude-opus-4-6.20260615") — the suffix must
// be stripped only when the prefix is a real table entry, so genuinely
// dotted model names such as gemini-2.5-pro survive intact.
func TestCanonical_DotSeparatedVersionSuffix(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"dated anthropic build", "claude-opus-4-6.20260615", "claude-opus-4-6"},
		{"dotted name that is itself a key", "gemini-2.5-pro", "gemini-2.5-pro"},
		{"dotted name with unknown prefix", "gemini-2.5-ultra", "gemini-2.5-ultra"},
		{"leading dot is not a suffix marker", ".claude-opus-4-6", ".claude-opus-4-6"},
		// The dot suffix is stripped before the vendor prefix, so a
		// combined "vertex/…-4-6.<date>" keeps its date: documenting the
		// current ordering rather than asserting an ideal.
		{"vendor prefix and dated build", "vertex/claude-opus-4-6.20260615", "claude-opus-4-6.20260615"},
		{"vendor prefix alone", "bedrock/claude-opus-4-6", "claude-opus-4-6"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, canonical(tc.model))
		})
	}
}

// TestEstimate_DatedBuildResolvesToBaseRate proves the canonicalisation
// above actually reaches the price table.
func TestEstimate_DatedBuildResolvesToBaseRate(t *testing.T) {
	u := agent.Usage{InputTokens: 1_000_000}
	dated, ok := Estimate(u, "claude-opus-4-6.20260615", nil)
	require := assert.New(t)
	require.True(ok)

	base, ok := Estimate(u, "claude-opus-4-6", nil)
	require.True(ok)
	require.InDelta(base, dated, 1e-9)
}
