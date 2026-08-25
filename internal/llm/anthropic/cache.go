package anthropic

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
)

// Ephemeral cache TTL choices exposed by the Anthropic API.
//
// Per Anthropic's 2026 caching guidance:
//   - **1-hour TTL** on the system prompt + tools block: these usually
//     don't change turn-to-turn, so a longer cache window amortises the
//     write cost over many completions.
//   - **5-minute TTL** on recent message turns: the conversation grows
//     with every reply, so a short TTL keeps memory-cost from ballooning
//     while still buying a discount on the immediate follow-up.
//
// Ordering matters: the 1-hour breakpoint must appear BEFORE the 5-min
// breakpoint in the request body (Anthropic scans the request front-to-
// back and invalidates the cache when it sees TTLs going backwards).
// Our client puts system+tools first, then messages, so we're naturally
// in the right order.
var (
	cacheEphemeral1h = sdk.CacheControlEphemeralParam{TTL: sdk.CacheControlEphemeralTTLTTL1h}
	cacheEphemeral5m = sdk.CacheControlEphemeralParam{TTL: sdk.CacheControlEphemeralTTLTTL5m}
)

// applyCacheMarkers sets ephemeral cache_control on the last content
// block of the last min(nMark, len(msgs)) messages. Anthropic caches
// the prefix up to and including any cache-marked block; putting the
// marker on the boundary between "stable" and "changing" content
// makes subsequent turns pay a fraction of the prompt cost.
//
// Messages get **5-minute TTL** — the message list changes with every
// turn so a longer TTL would spend cache creation on prefixes that get
// invalidated by the next reply. System prompt + tools get 1-hour TTL
// separately in the client (they don't change turn-to-turn).
//
// A no-op when nMark is 0. Silently caps at len(msgs) — callers that
// pass wildly large values still get a well-formed message list.
//
// This helper mutates msgs in place. It is idempotent — running it a
// second time replaces the CacheControl fields with the same value.
func applyCacheMarkers(msgs []sdk.MessageParam, nMark int) {
	if nMark <= 0 || len(msgs) == 0 {
		return
	}
	if nMark > len(msgs) {
		nMark = len(msgs)
	}
	start := len(msgs) - nMark
	for i := start; i < len(msgs); i++ {
		markLastTextBlock(&msgs[i])
	}
}

// markLastTextBlock walks the content blocks of a single MessageParam
// from the end backwards and sets 5-minute-TTL CacheControl on the
// first text block it finds. Tool_use / tool_result blocks are skipped
// — the SDK models them as different variants that carry their own
// optional CacheControl fields; text is the safe common denominator.
func markLastTextBlock(m *sdk.MessageParam) {
	blocks := m.Content
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].OfText == nil {
			continue
		}
		blocks[i].OfText.CacheControl = cacheEphemeral5m
		return
	}
}
