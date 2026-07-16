package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
)

// GmailListTool lists message ids matching a search query.
type GmailListTool struct{ c *Client }

// NewGmailListTool constructs a GmailListTool.
func NewGmailListTool(c *Client) *GmailListTool { return &GmailListTool{c: c} }

// Name implements tools.Tool.
func (*GmailListTool) Name() string { return "gmail_list" }

// Description implements tools.Tool.
func (*GmailListTool) Description() string {
	return "List Gmail message ids matching a query (Gmail search syntax, e.g. 'from:alice is:unread')."
}

// InputSchema implements tools.Tool.
func (*GmailListTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"q":           map[string]any{"type": "string"},
			"max_results": map[string]any{"type": "integer"},
		},
	}
}

// Execute implements tools.Tool.
func (t *GmailListTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Q          string `json:"q"`
		MaxResults int    `json:"max_results"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if args.MaxResults == 0 {
		args.MaxResults = 20
	}
	q := url.Values{}
	if args.Q != "" {
		q.Set("q", args.Q)
	}
	q.Set("maxResults", fmt.Sprintf("%d", args.MaxResults))
	u := t.c.gmailBase + "/users/me/messages?" + q.Encode()
	var out any
	if err := t.c.do(ctx, "GET", u, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// GmailGetTool fetches one message by id.
type GmailGetTool struct{ c *Client }

// NewGmailGetTool constructs a GmailGetTool.
func NewGmailGetTool(c *Client) *GmailGetTool { return &GmailGetTool{c: c} }

// Name implements tools.Tool.
func (*GmailGetTool) Name() string { return "gmail_get" }

// Description implements tools.Tool.
func (*GmailGetTool) Description() string {
	return "Fetch a single Gmail message by id. Returns headers + snippet + optional body."
}

// InputSchema implements tools.Tool.
func (*GmailGetTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":     map[string]any{"type": "string"},
			"format": map[string]any{"type": "string", "enum": []string{"metadata", "minimal", "full"}},
		},
		"required": []string{"id"},
	}
}

// Execute implements tools.Tool.
func (t *GmailGetTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct{ ID, Format string }
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.ID == "" {
		return "", fmt.Errorf("id is required")
	}
	if args.Format == "" {
		args.Format = "metadata"
	}
	q := url.Values{}
	q.Set("format", args.Format)
	u := t.c.gmailBase + "/users/me/messages/" + url.PathEscape(args.ID) + "?" + q.Encode()
	var out any
	if err := t.c.do(ctx, "GET", u, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// GmailSendTool sends a plain-text message.
type GmailSendTool struct{ c *Client }

// NewGmailSendTool constructs a GmailSendTool.
func NewGmailSendTool(c *Client) *GmailSendTool { return &GmailSendTool{c: c} }

// Name implements tools.Tool.
func (*GmailSendTool) Name() string { return "gmail_send" }

// Description implements tools.Tool.
func (*GmailSendTool) Description() string {
	return "Send a plain-text email via Gmail. Required: to, subject, body. Optional: from (defaults to authenticated user)."
}

// InputSchema implements tools.Tool.
func (*GmailSendTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"to":      map[string]any{"type": "string"},
			"subject": map[string]any{"type": "string"},
			"body":    map[string]any{"type": "string"},
			"from":    map[string]any{"type": "string"},
		},
		"required": []string{"to", "subject", "body"},
	}
}

// Execute implements tools.Tool.
func (t *GmailSendTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct{ To, Subject, Body, From string }
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.To == "" || args.Subject == "" || args.Body == "" {
		return "", fmt.Errorf("to, subject and body are required")
	}
	raw := buildRFC5322(args.From, args.To, args.Subject, args.Body)
	body := map[string]any{
		"raw": base64.URLEncoding.EncodeToString(raw),
	}
	var out any
	if err := t.c.do(ctx, "POST", t.c.gmailBase+"/users/me/messages/send", body, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// buildRFC5322 renders a minimal, plain-text RFC 5322 message.
func buildRFC5322(from, to, subject, body string) []byte {
	var b []byte
	if from != "" {
		b = append(b, []byte("From: "+from+"\r\n")...)
	}
	b = append(b, []byte("To: "+to+"\r\n")...)
	b = append(b, []byte("Subject: "+subject+"\r\n")...)
	b = append(b, []byte("Content-Type: text/plain; charset=utf-8\r\n")...)
	b = append(b, []byte("\r\n")...)
	b = append(b, []byte(body)...)
	return b
}
