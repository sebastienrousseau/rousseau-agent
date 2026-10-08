package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/mail"
	"net/url"
	"strings"
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

// Outbound implements tools.Outbound: it sends an email.
func (*GmailSendTool) Outbound() bool { return true }

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
	raw, err := buildRFC5322(args.From, args.To, args.Subject, args.Body)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"raw": base64.URLEncoding.EncodeToString(raw),
	}
	var out any
	if err := t.c.do(ctx, "POST", t.c.gmailBase+"/users/me/messages/send", body, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// buildRFC5322 renders a minimal, plain-text RFC 5322 message. Every
// header value is model-controlled, so each is checked for CR/LF (header
// injection, CWE-93), addresses are parsed and re-rendered, and the
// subject is RFC 2047 encoded.
func buildRFC5322(from, to, subject, body string) ([]byte, error) {
	for name, v := range map[string]string{"from": from, "to": to, "subject": subject} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("%s must not contain CR or LF", name)
		}
	}
	toHdr, err := formatAddressList(to)
	if err != nil {
		return nil, fmt.Errorf("invalid to address: %w", err)
	}
	var sb strings.Builder
	if from != "" {
		fromAddr, err := mail.ParseAddress(from)
		if err != nil {
			return nil, fmt.Errorf("invalid from address: %w", err)
		}
		sb.WriteString("From: " + formatAddress(fromAddr) + "\r\n")
	}
	sb.WriteString("To: " + toHdr + "\r\n")
	sb.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String()), nil
}

// formatAddressList parses a comma-separated address list and renders it
// back in canonical form.
func formatAddressList(list string) (string, error) {
	addrs, err := mail.ParseAddressList(list)
	if err != nil {
		return "", err
	}
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = formatAddress(a)
	}
	return strings.Join(out, ", "), nil
}

// formatAddress renders a parsed address, keeping a bare addr-spec bare.
func formatAddress(a *mail.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return a.String()
}
