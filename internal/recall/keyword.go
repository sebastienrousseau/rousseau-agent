package recall

import (
	"strings"
	"unicode"
)

// SimpleKeywordScorer is a tiny bag-of-words scorer suitable as
// [KeywordScorer]. Returns a value in [0, 1] based on the fraction of
// query terms that appear in the text. Cheap; no dependency on FTS5.
//
// Callers wanting SQLite FTS5's ranking should supply a KeywordScorer
// closure that calls into the search table.
func SimpleKeywordScorer(query, text string) float32 {
	qterms := tokenize(query)
	if len(qterms) == 0 {
		return 0
	}
	lowered := strings.ToLower(text)
	matches := 0
	for _, t := range qterms {
		if strings.Contains(lowered, t) {
			matches++
		}
	}
	return float32(matches) / float32(len(qterms))
}

func tokenize(s string) []string {
	var out []string
	var current []rune
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
			continue
		}
		if len(current) > 0 {
			out = appendUnique(out, string(current))
			current = current[:0]
		}
	}
	if len(current) > 0 {
		out = appendUnique(out, string(current))
	}
	return out
}

func appendUnique(out []string, s string) []string {
	for _, e := range out {
		if e == s {
			return out
		}
	}
	return append(out, s)
}
