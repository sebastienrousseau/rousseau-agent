// Package pricing computes USD cost estimates for a completion given
// its [agent.Usage] and the model that was used.
//
// The price table is baked into the binary at release time (see
// [DefaultTable]). Operators wanting to override — e.g. because their
// Anthropic contract has custom rates, or because a new model shipped
// between releases — can pass their own [Table] to [Estimate]. A CLI
// helper (`rousseau doctor --refresh-prices`) is planned but not yet
// implemented; the baked-in table is authoritative until then.
//
// All prices are per **one million** tokens, denominated in USD.
package pricing

import (
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Rate is the per-million-token USD price for one dimension of usage.
type Rate struct {
	// InputPerMTok is the base per-1M input-token price for cache
	// misses (a.k.a. "prompt tokens" in most billing dashboards).
	InputPerMTok float64
	// OutputPerMTok is the per-1M output-token price.
	OutputPerMTok float64
	// CacheWriteEphemeral5mPerMTok is the per-1M price for creating a
	// 5-minute-TTL cache entry (typically 1.25× the base input rate).
	// Zero means "use InputPerMTok" (the provider doesn't have a
	// separate cache-creation charge).
	CacheWriteEphemeral5mPerMTok float64
	// CacheWriteEphemeral1hPerMTok is the per-1M price for creating a
	// 1-hour-TTL cache entry (typically 2× the base input rate).
	CacheWriteEphemeral1hPerMTok float64
	// CacheReadPerMTok is the per-1M price for reading from any
	// existing cache entry (typically 0.1× the base input rate — the
	// point of caching).
	CacheReadPerMTok float64
}

// Table maps a canonical model identifier to its rate.
type Table map[string]Rate

// DefaultTable is the price table baked in at release time. Prices
// pulled from vendor pricing pages as of 2026-08. Fill in more as
// needed; missing models fall back to Estimate returning (0, false)
// so callers can degrade gracefully rather than compute a wrong number.
var DefaultTable = Table{
	// -- Anthropic Claude --
	// https://www.anthropic.com/pricing (Aug 2026)
	"claude-opus-4-6": {
		InputPerMTok:                 15.00,
		OutputPerMTok:                75.00,
		CacheWriteEphemeral5mPerMTok: 18.75, // 1.25×
		CacheWriteEphemeral1hPerMTok: 30.00, // 2×
		CacheReadPerMTok:             1.50,  // 0.1×
	},
	"claude-opus-4-7": {
		InputPerMTok:                 15.00,
		OutputPerMTok:                75.00,
		CacheWriteEphemeral5mPerMTok: 18.75,
		CacheWriteEphemeral1hPerMTok: 30.00,
		CacheReadPerMTok:             1.50,
	},
	"claude-sonnet-4-6": {
		InputPerMTok:                 3.00,
		OutputPerMTok:                15.00,
		CacheWriteEphemeral5mPerMTok: 3.75,
		CacheWriteEphemeral1hPerMTok: 6.00,
		CacheReadPerMTok:             0.30,
	},
	"claude-haiku-4-5": {
		InputPerMTok:                 1.00,
		OutputPerMTok:                5.00,
		CacheWriteEphemeral5mPerMTok: 1.25,
		CacheWriteEphemeral1hPerMTok: 2.00,
		CacheReadPerMTok:             0.10,
	},

	// -- OpenAI (Aug 2026 pricing page) --
	"gpt-5":       {InputPerMTok: 5.00, OutputPerMTok: 20.00},
	"gpt-5-mini":  {InputPerMTok: 0.50, OutputPerMTok: 2.00},
	"gpt-5-nano":  {InputPerMTok: 0.10, OutputPerMTok: 0.40},

	// -- Google Vertex (Aug 2026) --
	"gemini-2.5-pro":   {InputPerMTok: 3.50, OutputPerMTok: 10.50},
	"gemini-2.5-flash": {InputPerMTok: 0.35, OutputPerMTok: 1.05},
}

// Estimate returns the USD cost of usage on model, using table. When
// the model isn't in table, returns (0, false) — the caller decides
// whether "unknown model" means "cost is zero" (fine for tests) or
// "abort" (production).
//
// Cache tokens are billed at their own rates:
//   - CacheReadInputTokens          × CacheReadPerMTok
//   - CacheCreationEphemeral1h      × CacheWriteEphemeral1hPerMTok
//   - CacheCreationEphemeral5m      × CacheWriteEphemeral5mPerMTok
//   - unattributed CacheCreation    × CacheWriteEphemeral5mPerMTok (default TTL)
//   - InputTokens (non-cache)       × InputPerMTok
//   - OutputTokens                  × OutputPerMTok
func Estimate(u agent.Usage, model string, table Table) (float64, bool) {
	if table == nil {
		table = DefaultTable
	}
	rate, ok := table[canonical(model)]
	if !ok {
		return 0, false
	}
	cost := 0.0
	cost += perMillion(u.InputTokens, rate.InputPerMTok)
	cost += perMillion(u.OutputTokens, rate.OutputPerMTok)
	cost += perMillion(u.CacheReadInputTokens, rate.CacheReadPerMTok)

	// Per-TTL cache-creation billing. Anything the provider didn't
	// attribute to a bucket goes at the 5-minute rate (the API default
	// TTL when the caller doesn't set one).
	cost += perMillion(u.CacheCreationEphemeral1h, rate.CacheWriteEphemeral1hPerMTok)
	cost += perMillion(u.CacheCreationEphemeral5m, rate.CacheWriteEphemeral5mPerMTok)
	residual := u.CacheCreationInputTokens - u.CacheCreationEphemeral1h - u.CacheCreationEphemeral5m
	if residual > 0 {
		cost += perMillion(residual, rate.CacheWriteEphemeral5mPerMTok)
	}
	return cost, true
}

func perMillion(tokens int, ratePerMTok float64) float64 {
	if tokens <= 0 || ratePerMTok <= 0 {
		return 0
	}
	return float64(tokens) * ratePerMTok / 1_000_000
}

// canonical strips vendor prefixes and version suffixes so a config
// value like "anthropic:claude-opus-4-6:latest" or
// "claude-opus-4-6@20260615" still resolves. First it tries the raw
// name; if that misses, it strips characters after ":" and "@" and
// tries again.
func canonical(model string) string {
	if _, ok := DefaultTable[model]; ok {
		return model
	}
	trimmed := model
	if i := strings.IndexAny(trimmed, "@:"); i >= 0 {
		trimmed = trimmed[:i]
	}
	// Some deployments use dot-separated versions ("claude-opus-4-6.20260615").
	if i := strings.Index(trimmed, "."); i > 0 {
		if _, ok := DefaultTable[trimmed[:i]]; ok {
			trimmed = trimmed[:i]
		}
	}
	// Some vendor prefixes are "vertex/…" or "bedrock/…" — drop them.
	if i := strings.LastIndex(trimmed, "/"); i >= 0 && i < len(trimmed)-1 {
		trimmed = trimmed[i+1:]
	}
	return trimmed
}
