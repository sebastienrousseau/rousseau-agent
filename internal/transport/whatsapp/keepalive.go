//go:build !no_whatsmeow

package whatsapp

import (
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow"
)

// reconnectBackoff paces retries after a failed forced reconnect; the
// last delay repeats until a dial succeeds. Package-level for test
// overrides.
var reconnectBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute}

// keepaliveMissThreshold is the number of consecutive KeepAliveTimeout
// events (no KeepAliveRestored between them) that triggers a forced
// reconnect. Package-level for test overrides. Three matches the
// observed cadence: whatsmeow's default keepalive interval is ~30s, so
// three misses in a row is ~90s of an unresponsive server before we
// give up on the current socket — long enough to avoid churn on a
// single dropped ping, short enough to recover before an operator
// notices.
var keepaliveMissThreshold = 3

// handleKeepAliveTimeout counts a single missed ping. When the
// consecutive miss count crosses keepaliveMissThreshold and no
// reconnect is already in flight, it kicks off a Disconnect+Connect
// on a fresh goroutine so the whatsmeow event dispatcher is never
// blocked by a network round-trip.
func (c *Client) handleKeepAliveTimeout() {
	c.mu.Lock()
	c.keepaliveMisses++
	misses := c.keepaliveMisses
	wm := c.wm
	shouldReconnect := misses >= keepaliveMissThreshold && !c.reconnecting && wm != nil
	if shouldReconnect {
		c.reconnecting = true
	}
	c.mu.Unlock()

	c.logger.Warn("whatsapp.keepalive_timeout",
		slog.Int("consecutive", misses),
		slog.Int("threshold", keepaliveMissThreshold))

	if shouldReconnect {
		c.logger.Error("whatsapp.keepalive_reconnect",
			slog.Int("consecutive", misses),
			slog.String("action", "Disconnect + Connect"))
		go c.forceReconnect(wm)
	}
}

// handleKeepAliveRestored clears the miss counter and logs at INFO
// only when we were actually degraded. Zero-to-zero transitions stay
// silent so a healthy connection's occasional Restored events (which
// whatsmeow does not currently emit but might in future versions)
// never spam the log.
func (c *Client) handleKeepAliveRestored() {
	c.mu.Lock()
	prev := c.keepaliveMisses
	c.keepaliveMisses = 0
	c.mu.Unlock()
	if prev > 0 {
		c.logger.Info("whatsapp.keepalive_restored",
			slog.Int("recovered_after", prev))
	}
}

// forceReconnect closes the current whatsmeow socket and dials a
// fresh one. Called from its own goroutine so the onEvent dispatcher
// stays unblocked. A failed dial leaves no socket, so no keepalive
// loop runs and no later KeepAliveTimeout will ask again: it retries
// on reconnectBackoff until a dial succeeds or the client stops.
// Always clears reconnecting + keepaliveMisses on the way out.
func (c *Client) forceReconnect(wm *whatsmeow.Client) {
	// wm.Disconnect emits no Disconnected event; mark the session down
	// here so the health check fails until Connected arrives.
	c.setLinked(false)
	ok := c.reconnectUntilUp(wm)
	c.mu.Lock()
	c.reconnecting = false
	c.keepaliveMisses = 0
	c.mu.Unlock()
	if ok {
		c.logger.Info("whatsapp.reconnect_issued")
	}
}

// reconnectUntilUp reports whether a dial succeeded; false means the
// client stopped first.
func (c *Client) reconnectUntilUp(wm *whatsmeow.Client) bool {
	for attempt := 0; !c.isStopped(); attempt++ {
		err := wmForceReconnect(wm)
		if err == nil {
			return true
		}
		delay := reconnectBackoff[min(attempt, len(reconnectBackoff)-1)]
		c.logger.Error("whatsapp.reconnect_failed",
			slog.String("err", err.Error()),
			slog.Int("attempt", attempt+1),
			slog.Duration("retry_in", delay))
		time.Sleep(delay)
	}
	return false
}

func (c *Client) isStopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}
