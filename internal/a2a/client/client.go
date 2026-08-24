// Package client is the A2A client-side surface — used by a rousseau
// daemon to dispatch tasks to a peer agent and receive updates.
//
// # Status
//
// Scaffold only — types + interface are in place; the HTTP transport
// implementation is a follow-up ticket. See
// [docs/a2a.md](../../../docs/a2a.md).
package client

import (
	"context"
	"errors"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// Config configures a per-peer A2A client.
type Config struct {
	// Name identifies the peer in logs and metrics.
	Name string
	// Endpoint is the peer's A2A URL (e.g. https://peer.example.com/a2a).
	Endpoint string
	// AuthHeader is the value of the Authorization header sent on
	// every request (typically "Bearer <token>"). Empty disables auth.
	AuthHeader string
	// Timeout bounds a single request. Zero uses 60s.
	Timeout time.Duration
}

// Client is one A2A peer connection. Multiple clients per daemon are
// expected — one per remote agent the daemon coordinates with.
type Client struct {
	cfg Config
}

// New constructs a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("a2a/client: Endpoint is required")
	}
	if cfg.Name == "" {
		return nil, errors.New("a2a/client: Name is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Client{cfg: cfg}, nil
}

// Name returns the configured peer name.
func (c *Client) Name() string { return c.cfg.Name }

// FetchCard resolves the peer's capability card via
// GET /.well-known/agent-capabilities.
//
// STATUS: not implemented.
func (c *Client) FetchCard(_ context.Context) (a2a.CapabilityCard, error) {
	return a2a.CapabilityCard{}, errors.New("a2a/client: FetchCard not implemented — track docs/a2a.md")
}

// SubmitTask posts a task to the peer. The returned channel emits
// [a2a.TaskUpdate] until Status=completed / failed / cancelled or
// ctx cancels.
//
// STATUS: not implemented.
func (c *Client) SubmitTask(_ context.Context, _ a2a.Task) (<-chan a2a.TaskUpdate, error) {
	return nil, errors.New("a2a/client: SubmitTask not implemented — track docs/a2a.md")
}
