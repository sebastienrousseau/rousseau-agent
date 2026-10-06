package sqlite

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

// RecallSearcher adapts a *Store to model.RecallSearcher. Kept in this
// package so importers don't need to know which package owns Search.
type RecallSearcher struct {
	Store *Store
}

// NewRecallSearcher constructs a RecallSearcher.
func NewRecallSearcher(s *Store) *RecallSearcher { return &RecallSearcher{Store: s} }

// Search satisfies model.RecallSearcher. Converts sqlite.SearchHit to
// model.SearchHit so the agent package stays independent of storage.
func (r *RecallSearcher) Search(ctx context.Context, query string, limit int) ([]model.SearchHit, error) {
	hits, err := r.Store.Search(ctx, query, SearchOptions{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]model.SearchHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, model.SearchHit{SessionID: h.SessionID, Title: h.Title, Snippet: h.Snippet})
	}
	return out, nil
}
