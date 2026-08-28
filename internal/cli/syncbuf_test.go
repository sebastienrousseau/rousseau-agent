package cli

import (
	"bytes"
	"sync"
)

// syncBuffer is a bytes.Buffer guarded by a mutex.
//
// Tests that hand a *slog.Logger to code which spawns goroutines cannot
// share a bare bytes.Buffer with it. The MCP client, for example, runs a
// stderr-drain goroutine for the lifetime of the subprocess and logs
// through the same logger, so a test calling buf.String() races the
// drain goroutine's Write.
//
// That race is real but timing-dependent: it went unseen on linux/amd64
// and surfaced on macOS/arm64 in CI. Sharing a buffer with a logger is
// only safe when the buffer serialises access itself.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write satisfies io.Writer.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
