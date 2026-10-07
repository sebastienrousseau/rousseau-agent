package cli

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// buildCompressor translates the CompressionConfig to an
// agent.Compressor. Nil provider (should never happen in practice)
// falls through to NoopCompressor rather than panicking.
func buildCompressor(cfg config.CompressionConfig, provider agent.Provider) agent.Compressor {
	if !cfg.Enabled || provider == nil {
		return agent.NoopCompressor{}
	}
	trigger := cfg.TriggerMessages
	if trigger == 0 {
		trigger = 60
	}
	keep := cfg.KeepRecent
	if keep == 0 {
		keep = 8
	}
	tokens := cfg.TriggerTokens
	if tokens == 0 {
		tokens = 120_000
	}
	return &agent.LLMCompressor{
		Provider:        provider,
		TriggerMessages: trigger,
		TriggerTokens:   tokens,
		KeepRecent:      keep,
		SummaryPrompt:   cfg.Prompt,
	}
}
