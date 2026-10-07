package transport

import (
	"strings"
	"unicode/utf8"
)

// SplitReply cuts text into pieces of at most limit bytes so a reply
// longer than a platform's message cap (Telegram 4096, Discord 2000,
// …) goes out as several messages instead of one rejected call. It
// prefers to cut at a blank line, then a line break, then a space,
// and never inside a UTF-8 sequence. limit <= 0 or text within the
// limit returns text unchanged as the only piece; empty text returns
// nil.
func SplitReply(text string, limit int) []string {
	if text == "" {
		return nil
	}
	if limit <= 0 || len(text) <= limit {
		return []string{text}
	}
	var out []string
	for len(text) > limit {
		cut := splitPoint(text, limit)
		out = append(out, strings.TrimRight(text[:cut], "\n "))
		text = strings.TrimLeft(text[cut:], "\n ")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

// splitPoint picks the byte offset at which to cut text so the first
// piece is at most limit bytes and does not end mid-rune.
func splitPoint(text string, limit int) int {
	window := text[:limit]
	// Only honour a separator in the back half so a stray early
	// newline cannot produce a tiny first piece.
	floor := limit / 2
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(window, sep); i >= floor {
			return i + len(sep)
		}
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	if cut == 0 {
		// Only possible when limit is smaller than one rune; take the
		// whole rune rather than loop forever.
		_, size := utf8.DecodeRuneInString(text)
		return size
	}
	return cut
}
