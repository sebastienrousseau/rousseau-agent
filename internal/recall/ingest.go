package recall

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Ingester chunks + embeds + persists messages asynchronously. Push
// enqueues; the worker goroutine batches Embed calls and writes to
// the Store. Push never blocks on the embedder — when the queue is
// full, oldest jobs are dropped and a metric increments so operators
// can size the queue.
type Ingester struct {
	store    Store
	embedder Embedder
	logger   *slog.Logger

	chunkTokens  int
	chunkOverlap int
	batchSize    int

	mu    sync.Mutex
	queue []Job

	notify chan struct{}
	stop   chan struct{}
	done   chan struct{}

	maxQueue int
	dropped  int
}

// Job is one queued ingestion. A single agent.Message may become
// several Jobs after chunking; the caller controls chunk indexing.
type Job struct {
	SessionID  string
	MessageID  int64
	Role       string
	Text       string
	CreatedAt  time.Time
	ChunkIndex int
}

// IngesterConfig tunes the async worker.
type IngesterConfig struct {
	ChunkTokens  int // default 400
	ChunkOverlap int // default 40
	BatchSize    int // default 32
	MaxQueue     int // default 256
}

// NewIngester constructs an idle Ingester. Call Start to spawn the
// worker.
func NewIngester(store Store, embedder Embedder, cfg IngesterConfig, logger *slog.Logger) *Ingester {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ChunkTokens <= 0 {
		cfg.ChunkTokens = 400
	}
	if cfg.ChunkOverlap < 0 {
		cfg.ChunkOverlap = 40
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = 256
	}
	return &Ingester{
		store:        store,
		embedder:     embedder,
		logger:       logger,
		chunkTokens:  cfg.ChunkTokens,
		chunkOverlap: cfg.ChunkOverlap,
		batchSize:    cfg.BatchSize,
		notify:       make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		maxQueue:     cfg.MaxQueue,
	}
}

// Start spawns the worker goroutine.
func (i *Ingester) Start(ctx context.Context) {
	go i.loop(ctx)
}

// Stop signals the worker to drain and exit, blocking until it does.
func (i *Ingester) Stop() {
	close(i.stop)
	<-i.done
}

// Push enqueues the message text for asynchronous embedding. Chunking
// happens on the caller's goroutine because it's cheap and stops the
// queue from carrying redundant large strings.
func (i *Ingester) Push(sessionID string, messageID int64, role, text string, createdAt time.Time) {
	chunks := Chunk(text, i.chunkTokens, i.chunkOverlap)
	i.mu.Lock()
	for idx, c := range chunks {
		if len(i.queue) >= i.maxQueue {
			// Drop oldest.
			i.queue = i.queue[1:]
			i.dropped++
		}
		i.queue = append(i.queue, Job{
			SessionID: sessionID, MessageID: messageID, Role: role,
			Text: c, CreatedAt: createdAt, ChunkIndex: idx,
		})
	}
	i.mu.Unlock()

	select {
	case i.notify <- struct{}{}:
	default:
	}
}

// QueueDepth is the current pending-job count. Test helper.
func (i *Ingester) QueueDepth() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.queue)
}

// DroppedCount returns how many jobs were evicted due to a full
// queue. Test / metric helper.
func (i *Ingester) DroppedCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.dropped
}

func (i *Ingester) loop(ctx context.Context) {
	defer close(i.done)
	for {
		select {
		case <-i.stop:
			// Drain remaining queue before exit.
			i.drain(ctx)
			return
		case <-ctx.Done():
			return
		case <-i.notify:
			i.drain(ctx)
		}
	}
}

func (i *Ingester) drain(ctx context.Context) {
	for {
		batch := i.pull(i.batchSize)
		if len(batch) == 0 {
			return
		}
		texts := make([]string, len(batch))
		for k, j := range batch {
			texts[k] = j.Text
		}
		vecs, err := i.embedder.Embed(ctx, texts)
		if err != nil {
			i.logger.Warn("recall.embed_failed", slog.String("err", err.Error()))
			return
		}
		for k, j := range batch {
			row := Row{
				SessionID: j.SessionID, MessageID: j.MessageID,
				ChunkIndex: j.ChunkIndex, Role: j.Role, Text: j.Text,
				Embedding: vecs[k], CreatedAt: j.CreatedAt,
				Embedder: i.embedder.Name(),
			}
			if err := i.store.Put(ctx, row); err != nil {
				i.logger.Warn("recall.put_failed", slog.String("err", err.Error()))
			}
		}
	}
}

func (i *Ingester) pull(n int) []Job {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.queue) == 0 {
		return nil
	}
	if len(i.queue) < n {
		n = len(i.queue)
	}
	out := make([]Job, n)
	copy(out, i.queue[:n])
	i.queue = i.queue[n:]
	return out
}
