package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/recall"
)

// vectorRetriever is the narrow surface vectorRecall depends on;
// spelt out here rather than importing *recall.Retriever concretely
// so the CLI unit tests can inject a fake that returns fixture hits
// without wiring a real Store + Embedder.
type vectorRetriever interface {
	Recall(ctx context.Context, query string, k int) ([]recall.Hit, error)
}

// vectorRecall adapts a recall.Retriever to agent.RecallProvider so
// the daemon's turn loop can splice cross-session context into the
// system prompt exactly the way agent.FTSRecall does — but using
// vector cosine similarity plus optional keyword blending instead of
// SQLite FTS5 alone.
//
// Design mirrors agent.FTSRecall so operators reading `rousseau
// doctor` see the same knobs regardless of backend: Limit caps
// returned hits, SkipSessionID drops the current session's own
// recalls, empty results collapse to "" (leaves the prompt
// untouched).
type vectorRecall struct {
	retriever vectorRetriever
	// Limit caps hits per query. Zero uses 3 — matches FTSRecall.
	Limit int
	// SkipSessionID returns the ID to filter out of results. Nil skips
	// the filter (only the FTS variant ever set this in practice, but
	// keeping the seam identical avoids surprise when swapping
	// backends).
	SkipSessionID func(*agent.Session) string
	// TitleForHit lets the caller format the title heading in the
	// composed appendix. Nil falls back to "session <sessionID>".
	TitleForHit func(recall.Hit) string
}

// SystemAppendix satisfies agent.RecallProvider. It queries the
// retriever with the latest user text as the search key, filters the
// current session out (when SkipSessionID is set), and composes the
// hits into a Markdown appendix keyed identically to FTSRecall so a
// side-by-side swap does not change the prompt shape.
func (v *vectorRecall) SystemAppendix(ctx context.Context, s *agent.Session) string {
	if v == nil || v.retriever == nil {
		return ""
	}
	last, ok := lastUserText(s)
	if !ok {
		return ""
	}
	limit := v.Limit
	if limit == 0 {
		limit = 3
	}
	hits, err := v.retriever.Recall(ctx, last, limit)
	if err != nil || len(hits) == 0 {
		return ""
	}
	skip := ""
	if v.SkipSessionID != nil {
		skip = v.SkipSessionID(s)
	}
	kept := make([]recall.Hit, 0, len(hits))
	for _, h := range hits {
		if h.SessionID == skip {
			continue
		}
		kept = append(kept, h)
	}
	if len(kept) == 0 {
		return ""
	}
	return composeVectorRecall(kept, v.TitleForHit)
}

// lastUserText returns the newest user-role text block from s, if
// any. Duplicates agent.lastUserText (unexported there) rather than
// exporting a symbol just for this adapter.
func lastUserText(s *agent.Session) (string, bool) {
	if s == nil {
		return "", false
	}
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m.Role != agent.RoleUser {
			continue
		}
		var out strings.Builder
		for _, c := range m.Content {
			if c.Kind == agent.ContentText && c.Text != "" {
				if out.Len() > 0 {
					out.WriteByte('\n')
				}
				out.WriteString(c.Text)
			}
		}
		if out.Len() > 0 {
			return out.String(), true
		}
	}
	return "", false
}

func composeVectorRecall(hits []recall.Hit, titleFor func(recall.Hit) string) string {
	var b strings.Builder
	b.WriteString("\n\n# Related prior sessions\n\n")
	for _, h := range hits {
		title := ""
		if titleFor != nil {
			title = titleFor(h)
		}
		if title == "" {
			title = "session " + h.SessionID
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", title, h.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}
