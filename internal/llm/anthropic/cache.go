package anthropic

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
)

// applyCacheMarkers sets ephemeral cache_control on the last content
// block of the last min(nMark, len(msgs)) messages. Anthropic caches
// the prefix up to and including any cache-marked block; putting the
// marker on the boundary between "stable" and "changing" content
// makes subsequent turns pay a fraction of the prompt cost.
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
// from the end backwards and sets CacheControl on the first text block
// it finds. Tool_use / tool_result blocks are skipped — the SDK models
// them as different variants that carry their own optional
// CacheControl fields; text is the safe common denominator.
func markLastTextBlock(m *sdk.MessageParam) {
	blocks := m.Content
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].OfText == nil {
			continue
		}
		blocks[i].OfText.CacheControl = sdk.NewCacheControlEphemeralParam()
		return
	}
}
