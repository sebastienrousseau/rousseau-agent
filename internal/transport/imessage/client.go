// Package imessage implements transport.Transport against a
// BlueBubbles (https://bluebubbles.app) server. BlueBubbles is a
// macOS-side daemon that exposes iMessage via HTTP + Socket.IO.
//
// This client uses HTTP polling for receive (not Socket.IO) to
// minimise dependency surface — BlueBubbles's /api/v1/message endpoint
// returns a paginated newest-first list, and we track the most-recent
// message id we've forwarded so we only ship new ones to the handler.
package imessage

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
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Config configures the BlueBubbles-backed iMessage transport.
type Config struct {
	// BaseURL is the BlueBubbles server URL, e.g.
	// "http://localhost:1234". Required.
	BaseURL string
	// Password authenticates every request to BlueBubbles. Required.
	Password string
	// ReplyHeader is prepended to every outbound message.
	ReplyHeader string
	// PollInterval controls how often we ask BlueBubbles for new
	// messages. Zero uses 5s.
	PollInterval time.Duration
	// PageSize is the number of messages fetched per poll. Zero uses
	// 25 — plenty of headroom for a personal iMessage account.
	PageSize int
	// HTTPClient overrides the transport. Zero uses 30s timeout.
	HTTPClient *http.Client
}

// Client is a transport.Transport backed by BlueBubbles.
type Client struct {
	cfg     Config
	logger  *slog.Logger
	http    *http.Client
	stopped atomic.Bool

	mu     sync.Mutex
	lastID string // guid of the newest message we've already handled
}

// New constructs a Client. BaseURL + Password are required.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("imessage: BaseURL is required")
	}
	if cfg.Password == "" {
		return nil, errors.New("imessage: Password is required")
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.PageSize == 0 {
		cfg.PageSize = 25
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, logger: logger, http: cfg.HTTPClient}, nil
}

// Name returns the transport identifier.
func (*Client) Name() string { return "imessage" }

// Start polls BlueBubbles at PollInterval and routes newly-arrived
// messages from non-self senders to the handler.
func (c *Client) Start(ctx context.Context, handler transport.Handler) error {
	if handler == nil {
		return errors.New("imessage: handler is required")
	}
	c.logger.Info("imessage.started", slog.String("server", c.cfg.BaseURL))

	// Prime the last-seen cursor so we don't spam the handler with
	// everything in the user's message history on first launch.
	if err := c.primeCursor(ctx); err != nil {
		c.logger.Warn("imessage.prime_failed", slog.String("err", err.Error()))
	}

	tick := time.NewTicker(c.cfg.PollInterval)
	defer tick.Stop()
	for {
		if c.stopped.Load() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if err := c.pollOnce(ctx, handler); err != nil {
				c.logger.Warn("imessage.poll_failed", slog.String("err", err.Error()))
			}
		}
	}
}

// Stop halts the polling loop.
func (c *Client) Stop() error {
	c.stopped.Store(true)
	return nil
}

// Deliver sends a plain text message to a chat GUID.
func (c *Client) Deliver(ctx context.Context, chatGUID, body string) error {
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	payload := map[string]any{
		"chatGuid": chatGUID,
		"message":  body,
		"method":   "apple-script", // default for BlueBubbles
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("imessage: marshal: %w", err)
	}
	endpoint := c.cfg.BaseURL + "/api/v1/message/text?password=" + url.QueryEscape(c.cfg.Password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("imessage: build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("imessage: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("imessage: HTTP %d: %s", resp.StatusCode, truncate(string(rb), 400))
	}
	return nil
}

// primeCursor records the current newest message id without shipping
// anything to the handler.
func (c *Client) primeCursor(ctx context.Context) error {
	msgs, err := c.fetchMessages(ctx, 1)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}
	c.mu.Lock()
	c.lastID = msgs[0].GUID
	c.mu.Unlock()
	return nil
}

// pollOnce fetches the newest page of messages, filters to those we
// haven't already handled, and forwards non-self entries to the
// handler.
func (c *Client) pollOnce(ctx context.Context, handler transport.Handler) error {
	msgs, err := c.fetchMessages(ctx, c.cfg.PageSize)
	if err != nil {
		return err
	}
	c.mu.Lock()
	lastSeen := c.lastID
	c.mu.Unlock()

	// Newest-first from BlueBubbles; walk backwards so we forward
	// oldest-new-first to the handler.
	newestBoundary := 0
	for i, m := range msgs {
		if m.GUID == lastSeen {
			newestBoundary = i
			break
		}
	}
	fresh := msgs
	if newestBoundary > 0 {
		fresh = msgs[:newestBoundary]
	} else if lastSeen != "" {
		// The cursor drifted off — treat everything as fresh, but cap
		// at page size (BlueBubbles limits by design).
	}

	// Reverse to oldest-first.
	for i := len(fresh) - 1; i >= 0; i-- {
		m := fresh[i]
		if m.IsFromMe {
			continue
		}
		body := extractText(m)
		if body == "" {
			continue
		}
		msg := transport.IncomingMessage{
			From: m.Handle.Address,
			Body: body,
			At:   time.UnixMilli(m.DateCreated).UTC(),
		}
		c.logger.Info("imessage.incoming", slog.String("from", msg.From))
		reply, err := handler.Handle(ctx, msg)
		if err != nil {
			c.logger.Error("imessage.handler_failed", slog.String("err", err.Error()))
			continue
		}
		if reply == "" {
			continue
		}
		if err := c.Deliver(ctx, m.Chats[0].GUID, reply); err != nil {
			c.logger.Error("imessage.send_failed", slog.String("err", err.Error()))
		}
	}

	if len(msgs) > 0 {
		c.mu.Lock()
		c.lastID = msgs[0].GUID
		c.mu.Unlock()
	}
	return nil
}

// fetchMessages calls BlueBubbles's message endpoint. limit caps the
// paginated newest-first list.
func (c *Client) fetchMessages(ctx context.Context, limit int) ([]messageRecord, error) {
	q := url.Values{}
	q.Set("password", c.cfg.Password)
	q.Set("limit", fmt.Sprint(limit))
	q.Set("with", "chats,handle")
	q.Set("sort", "DESC")
	endpoint := c.cfg.BaseURL + "/api/v1/message?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("imessage: build: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imessage: get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("imessage: read: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("imessage: HTTP %d: %s", resp.StatusCode, truncate(string(rb), 400))
	}
	var out struct {
		Data []messageRecord `json:"data"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, fmt.Errorf("imessage: parse: %w", err)
	}
	return out.Data, nil
}

// extractText normalises the BlueBubbles message payload to a plain
// string body. Attachments are ignored.
func extractText(m messageRecord) string {
	if m.Text != "" {
		return m.Text
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// -- wire types --------------------------------------------------------

type messageRecord struct {
	GUID        string       `json:"guid"`
	Text        string       `json:"text"`
	IsFromMe    bool         `json:"isFromMe"`
	DateCreated int64        `json:"dateCreated"` // ms since epoch
	Handle      handleRecord `json:"handle"`
	Chats       []chatRecord `json:"chats"`
}

type handleRecord struct {
	Address string `json:"address"`
}

type chatRecord struct {
	GUID string `json:"guid"`
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
