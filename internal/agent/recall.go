package agent

import (
	"context"
	"fmt"
	"strings"
)

// RecallProvider looks up snippets from prior sessions relevant to the
// current user message and returns them as a system-prompt appendix.
// It is the cross-session analogue of SkillsProvider.
type RecallProvider interface {
	// SystemAppendix inspects s and returns text to append to the base
	// system prompt. Empty return leaves the prompt untouched.
	SystemAppendix(ctx context.Context, s *Session) string
}

// SearchHit is the shape a recall backend returns per matched session.
type SearchHit struct {
	SessionID string
	Title     string
	Snippet   string
}

// RecallSearcher is the narrow surface the FTS-backed recall provider
// depends on. Kept here (rather than importing state/sqlite) so tests
// can inject fakes without a real database.
type RecallSearcher interface {
	Search(ctx context.Context, query string, limit int) ([]SearchHit, error)
}

// FTSRecall implements RecallProvider by searching a FTS5 index of
// previous sessions using keywords from the latest user message.
type FTSRecall struct {
	Searcher RecallSearcher
	// Limit caps hits returned per query. Zero uses 3.
	Limit int
	// MinKeywordLen is the shortest word length considered. Zero uses 4.
	MinKeywordLen int
	// SkipSessionID is the current session's id — hits with this id
	// are dropped so the model does not recall its own history.
	SkipSessionID func(*Session) string
}

// SystemAppendix satisfies RecallProvider.
func (r *FTSRecall) SystemAppendix(ctx context.Context, s *Session) string {
	if r == nil || r.Searcher == nil {
		return ""
	}
	last, ok := lastUserText(s)
	if !ok {
		return ""
	}
	limit := r.Limit
	if limit == 0 {
		limit = 3
	}
	minLen := r.MinKeywordLen
	if minLen == 0 {
		minLen = 4
	}
	kw := keywords(last, minLen)
	if kw == "" {
		return ""
	}
	hits, err := r.Searcher.Search(ctx, kw, limit)
	if err != nil {
		return ""
	}
	skip := ""
	if r.SkipSessionID != nil {
		skip = r.SkipSessionID(s)
	}
	var out []SearchHit
	for _, h := range hits {
		if h.SessionID == skip {
			continue
		}
		out = append(out, h)
	}
	if len(out) == 0 {
		return ""
	}
	return composeRecall(out)
}

// keywords extracts a whitespace-joined keyword string suitable for
// FTS5 from the given user text. Words shorter than minLen are dropped;
// the OR combinator is inserted between terms so the searcher matches
// any of them.
func keywords(text string, minLen int) string {
	var out []string
	for _, w := range strings.Fields(text) {
		clean := strings.Trim(w, ".,;:?!'\"()[]{}<>")
		if len(clean) < minLen {
			continue
		}
		out = append(out, clean)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " OR ")
}

func composeRecall(hits []SearchHit) string {
	var b strings.Builder
	b.WriteString("\n\n# Related prior sessions\n\n")
	for _, h := range hits {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", h.Title, h.Snippet)
	}
	return strings.TrimRight(b.String(), "\n")
}

// lastUserText mirrors internal/skills.lastUserText but is scoped to
// this package so importers do not need both.
func lastUserText(s *Session) (string, bool) {
	if s == nil {
		return "", false
	}
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m.Role != RoleUser {
			continue
		}
		var out string
		for _, c := range m.Content {
			if c.Kind == ContentText && c.Text != "" {
				if out != "" {
					out += "\n"
				}
				out += c.Text
			}
		}
		if out != "" {
			return out, true
		}
	}
	return "", false
}
