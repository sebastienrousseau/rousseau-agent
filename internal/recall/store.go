package recall

import (
	"context"
	"sort"
	"time"
)

// Row is the projection of one embedded chunk suitable for retrieval.
type Row struct {
	ID         int64
	SessionID  string
	MessageID  int64
	ChunkIndex int
	Role       string
	Text       string
	Embedding  []float32
	CreatedAt  time.Time
	Embedder   string
}

// Store is the storage contract the retriever consumes. The sqlite
// implementation is in internal/state/sqlite/recall_vec.go; an
// in-memory fake lives in the tests.
type Store interface {
	Put(ctx context.Context, r Row) error
	// Since returns rows whose CreatedAt >= after, ordered ascending
	// by CreatedAt. after.IsZero() matches every row.
	Since(ctx context.Context, after time.Time) ([]Row, error)
	// PurgeOlderThan removes rows older than cutoff and returns the
	// count deleted.
	PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// Hit is one row returned by a hybrid retrieval.
type Hit struct {
	Row
	Score float32 // higher = more relevant
}

// Retriever runs a hybrid (vector + keyword) query against a Store.
// KeywordScorer is optional — when nil, only the vector cosine
// similarity contributes.
type Retriever struct {
	store      Store
	embedder   Embedder
	kwScorer   KeywordScorer
	weight     float32
	windowFrom time.Time
}

// KeywordScorer scores a candidate text against a query and returns a
// value in [0, 1]. A tiny FTS5-like ranker lives in the same package;
// callers can supply a bespoke one.
type KeywordScorer func(query, text string) float32

// NewRetriever wires the retriever. weight is the vector-vs-keyword
// blend: 1.0 = pure vector, 0.0 = pure keyword, 0.7 is the shipped
// default. Values outside [0, 1] are clamped.
func NewRetriever(store Store, e Embedder, kw KeywordScorer, weight float32) *Retriever {
	if weight < 0 {
		weight = 0
	}
	if weight > 1 {
		weight = 1
	}
	return &Retriever{store: store, embedder: e, kwScorer: kw, weight: weight}
}

// WithWindow restricts retrieval to rows created after t. Zero t
// covers every row (default).
func (r *Retriever) WithWindow(t time.Time) *Retriever {
	rc := *r
	rc.windowFrom = t
	return &rc
}

// Recall returns the top-k rows for the supplied query, using hybrid
// scoring. k ≤ 0 returns nothing.
func (r *Retriever) Recall(ctx context.Context, query string, k int) ([]Hit, error) {
	if k <= 0 {
		return nil, nil
	}
	// Embed the query.
	vecs, err := r.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, nil
	}
	qv := vecs[0]

	// Fetch candidate rows within the window. Retrieval is O(N × D);
	// see package doc.
	rows, err := r.store.Since(ctx, r.windowFrom)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	hits := make([]Hit, 0, len(rows))
	for _, row := range rows {
		if len(row.Embedding) == 0 {
			continue
		}
		vec := CosineSimilarity(qv, row.Embedding)
		var kw float32
		if r.kwScorer != nil {
			kw = r.kwScorer(query, row.Text)
		}
		blended := r.weight*vec + (1-r.weight)*kw
		hits = append(hits, Hit{Row: row, Score: blended})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}
