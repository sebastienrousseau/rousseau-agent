package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp"
)

// defaultDiscoverTimeout bounds the server/discover probe. A legacy
// server that silently ignores unknown methods costs this much once at
// startup before the client falls back to initialize.
const defaultDiscoverTimeout = 3 * time.Second

// RPCError is a JSON-RPC error a server returned for a request.
type RPCError struct {
	Server  string
	Method  string
	Code    int
	Message string
	Data    any
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("mcp/client %s: server returned error on %s: [%d] %s", e.Server, e.Method, e.Code, e.Message)
}

// errNoMutualVersion means the server and this client share no
// protocol revision.
var errNoMutualVersion = errors.New("no protocol version in common")

// handshake picks the protocol era the way 2026-07-28 prescribes for a
// client that also talks to older servers: probe with server/discover,
// and fall back to the initialize handshake on any error other than
// "unsupported protocol version" (legacy servers answer an unknown
// pre-initialize method in several ways, or not at all).
func (c *Client) handshake(ctx context.Context, discoverTimeout time.Duration) error {
	probeCtx, cancel := context.WithTimeout(ctx, discoverTimeout)
	res, err := c.discover(probeCtx)
	cancel()
	var rpcErr *RPCError
	switch {
	case err == nil:
		return c.adoptDiscover(ctx, res)
	case errors.As(err, &rpcErr) && rpcErr.Code == mcp.CodeUnsupportedProtocolVersion:
		return fmt.Errorf("%w: server supports %v, client speaks %s", errNoMutualVersion, rpcErr.Data, mcp.ModernProtocolVersion)
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		c.logger.Debug("mcp.client.discover_fallback", slog.String("reason", err.Error()))
		return c.initialize(ctx)
	}
}

// discover sends the server/discover probe.
func (c *Client) discover(ctx context.Context) (mcp.DiscoverResult, error) {
	var res mcp.DiscoverResult
	params, err := withMeta(nil, c.modernMeta())
	if err != nil {
		return res, err
	}
	err = c.request(ctx, mcp.MethodDiscover, params, &res)
	return res, err
}

// adoptDiscover switches to the stateless revision when the server
// offers it, and otherwise falls back to the initialize handshake: a
// result that lists only legacy revisions, or that is not a discover
// result at all (a loose legacy server answering any method), means
// the server wants the old handshake.
func (c *Client) adoptDiscover(ctx context.Context, res mcp.DiscoverResult) error {
	for _, v := range res.SupportedVersions {
		if v == mcp.ModernProtocolVersion {
			c.protocolVersion = v
			c.modern = true
			c.logger.Info("mcp.client.discovered", slog.String("protocol_version", v))
			return nil
		}
	}
	return c.initialize(ctx)
}

// modernMeta is the _meta every 2026-07-28 request carries. The client
// declares no capabilities, so a compliant server never asks it for
// input (sampling, elicitation, roots).
func (c *Client) modernMeta() map[string]any {
	return map[string]any{
		mcp.MetaProtocolVersion:    mcp.ModernProtocolVersion,
		mcp.MetaClientCapabilities: map[string]any{},
		mcp.MetaClientInfo:         map[string]any{"name": "rousseau-agent", "version": c.version},
	}
}

// withMeta returns params as a JSON object with meta merged into its
// _meta member. nil params become an object holding only _meta.
func withMeta(params any, meta map[string]any) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("params must be a JSON object: %w", err)
		}
	}
	merged := map[string]any{}
	if existing, ok := obj["_meta"]; ok {
		if err := json.Unmarshal(existing, &merged); err != nil {
			return nil, fmt.Errorf("_meta must be a JSON object: %w", err)
		}
	}
	for k, v := range meta {
		merged[k] = v
	}
	m, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	obj["_meta"] = m
	return json.Marshal(obj)
}

// checkResultType enforces the 2026-07-28 rule: a missing resultType
// means "complete", "input_required" asks for client input this client
// never declared it could give, and anything else is invalid.
func checkResultType(raw json.RawMessage) error {
	if len(raw) == 0 || raw[0] != '{' {
		return nil // non-object results are decoded (and rejected) by the caller
	}
	var probe struct {
		ResultType string `json:"resultType"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("decode resultType: %w", err)
	}
	switch probe.ResultType {
	case "", "complete":
		return nil
	case "input_required":
		return errors.New("server requires client input (sampling, elicitation or roots), which this client does not support")
	default:
		return fmt.Errorf("invalid resultType %q", probe.ResultType)
	}
}

// cancelRequest tells the server to stop working on request id. Both
// eras define notifications/cancelled; a server that already finished
// ignores it.
func (c *Client) cancelRequest(id int64, reason string) {
	if err := c.notify(mcp.MethodCancelled, map[string]any{"requestId": id, "reason": reason}); err != nil {
		c.logger.Debug("mcp.client.cancel_failed", slog.Int64("id", id), slog.String("err", err.Error()))
	}
}

// ProtocolVersion is the revision negotiated with the server.
func (c *Client) ProtocolVersion() string { return c.protocolVersion }
