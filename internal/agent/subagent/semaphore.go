package subagent

import "context"

// semaphore is a small counted mutex used by Spawn to cap
// concurrency. Buffered-channel implementation — no dependency on
// x/sync/semaphore keeps the module surface tight.
type semaphore struct {
	slots chan struct{}
}

func newSemaphore(n int) *semaphore {
	if n < 1 {
		n = 1
	}
	return &semaphore{slots: make(chan struct{}, n)}
}

// acquire blocks until a slot is available or ctx is done.
func (s *semaphore) acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns a slot.
func (s *semaphore) release() { <-s.slots }
