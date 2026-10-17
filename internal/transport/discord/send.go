package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxTextLen is Discord's hard cap on message content; longer replies
// go out as several messages.
const maxTextLen = 2000

// Deliver posts to a channel id. Suitable as a cron.Delivery target.
func (c *Client) Deliver(ctx context.Context, channelID, body string) error {
	return c.postMessage(ctx, channelID, body)
}

func (c *Client) postMessage(ctx context.Context, channelID, body string) error {
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	payload := map[string]any{"content": body}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("discord: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/channels/%s/messages", c.cfg.BaseURL, channelID), bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("discord: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+c.cfg.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("discord: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody)) //nolint:errcheck // best-effort read of error body
		return fmt.Errorf("discord: HTTP %d: %s", resp.StatusCode, truncate(string(rb), 400))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
