// Package recall re-exports the internal/recall surface so external
// modules can build hybrid vector+keyword recall without importing
// /internal.
package recall

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/recall"
)

// Embedder aliases [recall.Embedder].
type Embedder = recall.Embedder

// NoopEmbedder aliases [recall.NoopEmbedder].
type NoopEmbedder = recall.NoopEmbedder

// VoyageEmbedder aliases [recall.VoyageEmbedder].
type VoyageEmbedder = recall.VoyageEmbedder

// VoyageConfig aliases [recall.VoyageConfig].
type VoyageConfig = recall.VoyageConfig

// Row aliases [recall.Row].
type Row = recall.Row

// Hit aliases [recall.Hit].
type Hit = recall.Hit

// Store aliases [recall.Store].
type Store = recall.Store

// Retriever aliases [recall.Retriever].
type Retriever = recall.Retriever

// KeywordScorer aliases [recall.KeywordScorer].
type KeywordScorer = recall.KeywordScorer

// Ingester aliases [recall.Ingester].
type Ingester = recall.Ingester

// IngesterConfig aliases [recall.IngesterConfig].
type IngesterConfig = recall.IngesterConfig

// SQLiteStore aliases [recall.SQLiteStore].
type SQLiteStore = recall.SQLiteStore

// Direct function aliases.
var (
	NewVoyageEmbedder   = recall.NewVoyageEmbedder
	NewRetriever        = recall.NewRetriever
	NewIngester         = recall.NewIngester
	NewSQLiteStore      = recall.NewSQLiteStore
	SimpleKeywordScorer = recall.SimpleKeywordScorer
	EncodeVector        = recall.EncodeVector
	DecodeVector        = recall.DecodeVector
	CosineSimilarity    = recall.CosineSimilarity
	Chunk               = recall.Chunk
	ValidateVector      = recall.ValidateVector
)

// Sentinels.
var (
	ErrDimensionMismatch = recall.ErrDimensionMismatch
)
