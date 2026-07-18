// Package main demonstrates the hybrid recall primitive
// (pkg/recall). Ingests a handful of messages, embeds them with
// the Noop embedder (real deployments swap in Voyage / OpenAI /
// Ollama), and runs a semantic-plus-keyword query.
//
// Run with:
//
//	go run ./examples/embed-recall
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/pkg/recall"
	sqlitestate "github.com/sebastienrousseau/rousseau-agent/pkg/state/sqlite"
)

func main() {
	ctx := context.Background()

	store, err := sqlitestate.Open(ctx, ":memory:")
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	inner, err := sqlitestate.NewRecallVectors(ctx, store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recall_vectors: %v\n", err)
		os.Exit(1)
	}

	// Real deployments substitute VoyageEmbedder / OpenAIEmbedder;
	// noop keeps this example runnable without an API key.
	embedder := recall.NoopEmbedder{D: 8}
	rstore := recall.NewSQLiteStore(inner, embedder.Dims())

	// Ingest a handful of messages the way a daemon would.
	corpus := []struct {
		id  int64
		msg string
	}{
		{1, "the whatsapp transport fires QR pairing on first launch"},
		{2, "slack socket mode uses xapp- + xoxb- token pair"},
		{3, "signal transport shells out to signal-cli in JSON-RPC mode"},
		{4, "matrix homeserver URL + access token wire into the room stream"},
	}
	for _, row := range corpus {
		vecs, _ := embedder.Embed(ctx, []string{row.msg})
		_ = rstore.Put(ctx, recall.Row{
			SessionID:  "s1",
			MessageID:  row.id,
			ChunkIndex: 0,
			Role:       "user",
			Text:       row.msg,
			Embedding:  vecs[0],
			CreatedAt:  time.Now().UTC(),
			Embedder:   embedder.Name(),
		})
	}

	// Hybrid retrieve — 70% vector, 30% keyword, top-2.
	retriever := recall.NewRetriever(rstore, embedder, recall.SimpleKeywordScorer, 0.7)
	hits, err := retriever.Recall(ctx, "how do I pair signal", 2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recall: %v\n", err)
		os.Exit(1)
	}
	for _, h := range hits {
		fmt.Printf("[%.3f] %s\n", h.Score, h.Text)
	}
}
