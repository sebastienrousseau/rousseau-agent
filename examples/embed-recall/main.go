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
	"io"
	"os"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/pkg/recall"
	sqlitestate "github.com/sebastienrousseau/rousseau-agent/pkg/state/sqlite"
)

func main() { os.Exit(run(context.Background(), os.Stdout, os.Stderr)) }

// run executes the demo and returns the process exit code. main does
// nothing but call os.Exit so that tests can drive run directly.
func run(ctx context.Context, out, errOut io.Writer) int {
	if err := demo(ctx, out); err != nil {
		fmt.Fprintln(errOut, "embed-recall:", err)
		return 1
	}
	return 0
}

func demo(ctx context.Context, out io.Writer) error {
	store, err := sqlitestate.Open(ctx, ":memory:")
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = store.Close() }()

	inner, err := sqlitestate.NewRecallVectors(ctx, store)
	if err != nil {
		return fmt.Errorf("recall_vectors: %w", err)
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
		vecs, err := embedder.Embed(ctx, []string{row.msg})
		if err != nil {
			return fmt.Errorf("embed %d: %w", row.id, err)
		}
		if err := rstore.Put(ctx, recall.Row{
			SessionID:  "s1",
			MessageID:  row.id,
			ChunkIndex: 0,
			Role:       "user",
			Text:       row.msg,
			Embedding:  vecs[0],
			CreatedAt:  time.Now().UTC(),
			Embedder:   embedder.Name(),
		}); err != nil {
			return fmt.Errorf("put %d: %w", row.id, err)
		}
	}

	// Hybrid retrieve — 70% vector, 30% keyword, top-2.
	retriever := recall.NewRetriever(rstore, embedder, recall.SimpleKeywordScorer, 0.7)
	hits, err := retriever.Recall(ctx, "how do I pair signal", 2)
	if err != nil {
		return fmt.Errorf("recall: %w", err)
	}
	for _, h := range hits {
		fmt.Fprintf(out, "[%.3f] %s\n", h.Score, h.Text)
	}
	return nil
}
