// Package matrix implements transport.Transport against Matrix's
// client-server API (Synapse and any spec-compliant homeserver).
//
// Uses long-polling `/sync` for inbound and `/rooms/{room}/send` for
// outbound. No third-party SDK — the auth/sync protocol is small and
// pulling in mautrix or gomatrix triples our dep footprint.
package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Config configures the Matrix transport.
type Config struct {
	// HomeserverURL is the base URL, e.g. "https://matrix.org".
	HomeserverURL string
	// AccessToken is the bot user's access token (from
	// /login or /register). Required.
	AccessToken string
	// UserID is the bot user's full MXID ("@bot:matrix.org"). Used
	// to skip own-message echo. Optional but strongly recommended.
	UserID string
	// ReplyHeader is prepended to every outbound message.
	ReplyHeader string
	// PollTimeout controls the /sync long-poll. Zero uses 30s.
	PollTimeout time.Duration
	// HTTPClient overrides the transport. Zero uses a 60s-timeout
	// client. Tests inject httptest-backed clients here.
	HTTPClient *http.Client
}

// Client is a transport.Transport backed by the Matrix client-server
// API.
type Client struct {
	cfg     Config
	logger  *slog.Logger
	http    *http.Client
	stopped atomic.Bool

	mu    sync.Mutex
	since string // opaque cursor returned by the last /sync
}

// New constructs a Client. HomeserverURL + AccessToken are required.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.HomeserverURL == "" {
		return nil, errors.New("matrix: HomeserverURL is required")
	}
	if cfg.AccessToken == "" {
		return nil, errors.New("matrix: AccessToken is required")
	}
	if cfg.PollTimeout == 0 {
		cfg.PollTimeout = 30 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, logger: logger, http: cfg.HTTPClient}, nil
}

// Name returns the transport identifier.
func (*Client) Name() string { return "matrix" }

// Start begins long-polling for events and routes m.room.message
// events to handler until ctx is cancelled or Stop is called.
func (c *Client) Start(ctx context.Context, handler transport.Handler) error {
	if handler == nil {
		return errors.New("matrix: handler is required")
	}
	c.logger.Info("matrix.started", slog.String("homeserver", c.cfg.HomeserverURL))
	for {
		if c.stopped.Load() || ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := c.sync(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.logger.Warn("matrix.sync_failed", slog.String("err", err.Error()))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
				continue
			}
		}
		c.route(ctx, resp, handler)
	}
}

// Stop halts the sync loop on the next iteration.
func (c *Client) Stop() error {
	c.stopped.Store(true)
	return nil
}

// Deliver sends a plain text message to the given room id (as a
// string). Suitable as a cron.Delivery target.
func (c *Client) Deliver(ctx context.Context, roomID, body string) error {
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	payload := map[string]any{
		"msgtype": "m.text",
		"body":    body,
	}
	txn := strconv.FormatInt(time.Now().UnixNano(), 10)
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s",
		c.cfg.HomeserverURL, url.PathEscape(roomID), url.PathEscape(txn))
	return c.doPUT(ctx, endpoint, payload, nil)
}

// sync issues a long-poll /sync request.
func (c *Client) sync(ctx context.Context) (*syncResponse, error) {
	c.mu.Lock()
	since := c.since
	c.mu.Unlock()

	q := url.Values{}
	q.Set("timeout", strconv.FormatInt(c.cfg.PollTimeout.Milliseconds(), 10))
	if since != "" {
		q.Set("since", since)
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/sync?%s", c.cfg.HomeserverURL, q.Encode())

	var resp syncResponse
	if err := c.doGET(ctx, endpoint, &resp); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.since = resp.NextBatch
	c.mu.Unlock()
	return &resp, nil
}

// route walks the sync response and forwards each m.room.message from
// a non-self sender to the handler.
func (c *Client) route(ctx context.Context, resp *syncResponse, handler transport.Handler) {
	for roomID, room := range resp.Rooms.Join {
		for _, evt := range room.Timeline.Events {
			if evt.Type != "m.room.message" {
				continue
			}
			if c.cfg.UserID != "" && evt.Sender == c.cfg.UserID {
				continue // loop prevention
			}
			body := extractBody(evt.Content)
			if body == "" {
				continue
			}
			msg := transport.IncomingMessage{
				From: evt.Sender,
				Body: body,
				At:   time.Unix(evt.OriginServerTS/1000, (evt.OriginServerTS%1000)*int64(time.Millisecond)),
			}
			c.logger.Info("matrix.incoming",
				slog.String("from", msg.From),
				slog.String("room", roomID))
			reply, err := handler.Handle(ctx, msg)
			if err != nil {
				c.logger.Error("matrix.handler_failed", slog.String("err", err.Error()))
				continue
			}
			if reply == "" {
				continue
			}
			if err := c.Deliver(ctx, roomID, reply); err != nil {
				c.logger.Error("matrix.send_failed", slog.String("err", err.Error()))
			}
		}
	}
}

// extractBody pulls the text body out of an m.room.message content
// payload. Only m.text is honoured today; m.notice / m.emote could be
// added if there is user demand.
func extractBody(raw json.RawMessage) string {
	var content struct {
		MsgType string `json:"msgtype"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal(raw, &content); err != nil {
		return ""
	}
	if content.MsgType != "m.text" {
		return ""
	}
	return content.Body
}

// -- HTTP helpers ------------------------------------------------------

func (c *Client) doGET(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("matrix: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	return c.do(req, out)
}

func (c *Client) doPUT(ctx context.Context, endpoint string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("matrix: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("matrix: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("matrix: %s: %w", req.Method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("matrix: read: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("matrix: HTTP %d: %s", resp.StatusCode, truncate(string(body), 400))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("matrix: parse: %w", err)
	}
	return nil
}

// -- wire types --------------------------------------------------------

type syncResponse struct {
	NextBatch string    `json:"next_batch"`
	Rooms     roomsData `json:"rooms"`
}

type roomsData struct {
	Join map[string]joinedRoom `json:"join"`
}

type joinedRoom struct {
	Timeline timeline `json:"timeline"`
}

type timeline struct {
	Events []timelineEvent `json:"events"`
}

type timelineEvent struct {
	Type           string          `json:"type"`
	Sender         string          `json:"sender"`
	EventID        string          `json:"event_id"`
	OriginServerTS int64           `json:"origin_server_ts"`
	Content        json.RawMessage `json:"content"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
