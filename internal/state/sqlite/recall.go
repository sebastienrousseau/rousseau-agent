package sqlite

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// RecallSearcher adapts a *Store to agent.RecallSearcher. Kept in this
// package so importers don't need to know which package owns Search.
type RecallSearcher struct {
	Store *Store
}

// NewRecallSearcher constructs a RecallSearcher.
func NewRecallSearcher(s *Store) *RecallSearcher { return &RecallSearcher{Store: s} }

// Search satisfies agent.RecallSearcher. Converts sqlite.SearchHit to
// agent.SearchHit so the agent package stays independent of storage.
func (r *RecallSearcher) Search(ctx context.Context, query string, limit int) ([]agent.SearchHit, error) {
	hits, err := r.Store.Search(ctx, query, SearchOptions{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]agent.SearchHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, agent.SearchHit{SessionID: h.SessionID, Title: h.Title, Snippet: h.Snippet})
	}
	return out, nil
}
