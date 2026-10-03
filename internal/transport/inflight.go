package transport

import "sync"

// Inflight runs per-message work off a transport's receive loop.
//
// A loop that calls the handler inline blocks on every agent turn:
// one sender's long turn stalls every other sender, and their /cancel
// or /status cannot even be read until it finishes. Handing each
// message to Go keeps the loop receiving; per-sender ordering and
// steering are the Supervisor's job, and the daemon's TurnLimiter caps
// how many turns actually run. Wait lets the loop drain in-flight work
// on shutdown so replies are not cut off mid-send.
//
// The zero value is ready to use. A nil *Inflight runs work inline,
// which lets transports keep unit tests that call their routing code
// directly synchronous: Start installs a non-nil one for the real loop.
type Inflight struct {
	wg sync.WaitGroup
}

// Go runs fn on its own goroutine, tracked for Wait.
func (f *Inflight) Go(fn func()) {
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		fn()
	}()
}

// Do runs fn concurrently via Go, or inline when f is nil.
func (f *Inflight) Do(fn func()) {
	if f == nil {
		fn()
		return
	}
	f.Go(fn)
}

// Wait blocks until every fn started by Go has returned. Nil-safe.
func (f *Inflight) Wait() {
	if f == nil {
		return
	}
	f.wg.Wait()
}
