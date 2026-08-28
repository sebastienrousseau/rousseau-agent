package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// -- post_message ----------------------------------------------------

// PostMessageTool posts a message to a channel or thread.
type PostMessageTool struct{ c *Client }

// NewPostMessageTool constructs a PostMessageTool.
func NewPostMessageTool(c *Client) *PostMessageTool { return &PostMessageTool{c: c} }

// Name implements tools.Tool.
func (*PostMessageTool) Name() string { return "slack_post_message" }

// Description implements tools.Tool.
func (*PostMessageTool) Description() string {
	return "Post a message to a Slack channel. Required: channel (id or name), text. Optional: thread_ts to reply in a thread."
}

// InputSchema implements tools.Tool.
func (*PostMessageTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"channel":   map[string]any{"type": "string"},
			"text":      map[string]any{"type": "string"},
			"thread_ts": map[string]any{"type": "string"},
		},
		"required": []string{"channel", "text"},
	}
}

// Execute implements tools.Tool.
func (t *PostMessageTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Channel  string `json:"channel"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Channel == "" || args.Text == "" {
		return "", fmt.Errorf("channel and text are required")
	}
	body := map[string]any{"channel": args.Channel, "text": args.Text}
	if args.ThreadTS != "" {
		body["thread_ts"] = args.ThreadTS
	}
	var out struct {
		OK      bool   `json:"ok"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := t.c.postJSON(ctx, "chat.postMessage", body, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// -- get_thread ------------------------------------------------------

// GetThreadTool reads replies to a threaded message.
type GetThreadTool struct{ c *Client }

// NewGetThreadTool constructs a GetThreadTool.
func NewGetThreadTool(c *Client) *GetThreadTool { return &GetThreadTool{c: c} }

// Name implements tools.Tool.
func (*GetThreadTool) Name() string { return "slack_get_thread" }

// Description implements tools.Tool.
func (*GetThreadTool) Description() string {
	return "Read replies on a Slack thread. Required: channel, thread_ts (the parent message's ts)."
}

// InputSchema implements tools.Tool.
func (*GetThreadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"channel":   map[string]any{"type": "string"},
			"thread_ts": map[string]any{"type": "string"},
		},
		"required": []string{"channel", "thread_ts"},
	}
}

// Execute implements tools.Tool.
func (t *GetThreadTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Channel == "" || args.ThreadTS == "" {
		return "", fmt.Errorf("channel and thread_ts are required")
	}
	q := url.Values{}
	q.Set("channel", args.Channel)
	q.Set("ts", args.ThreadTS)
	var out any
	if err := t.c.get(ctx, "conversations.replies", q, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// -- add_reaction ----------------------------------------------------

// AddReactionTool adds an emoji reaction to a message.
type AddReactionTool struct{ c *Client }

// NewAddReactionTool constructs an AddReactionTool.
func NewAddReactionTool(c *Client) *AddReactionTool { return &AddReactionTool{c: c} }

// Name implements tools.Tool.
func (*AddReactionTool) Name() string { return "slack_add_reaction" }

// Description implements tools.Tool.
func (*AddReactionTool) Description() string {
	return "Add an emoji reaction to a Slack message. Required: channel, timestamp, emoji name (no colons)."
}

// InputSchema implements tools.Tool.
func (*AddReactionTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"channel":   map[string]any{"type": "string"},
			"timestamp": map[string]any{"type": "string"},
			"name":      map[string]any{"type": "string", "description": "Emoji name without colons, e.g. 'thumbsup'."},
		},
		"required": []string{"channel", "timestamp", "name"},
	}
}

// Execute implements tools.Tool.
func (t *AddReactionTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct{ Channel, Timestamp, Name string }
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Channel == "" || args.Timestamp == "" || args.Name == "" {
		return "", fmt.Errorf("channel, timestamp and name are required")
	}
	form := url.Values{}
	form.Set("channel", args.Channel)
	form.Set("timestamp", args.Timestamp)
	form.Set("name", args.Name)
	if err := t.c.postForm(ctx, "reactions.add", form, nil); err != nil {
		return "", err
	}
	return `{"ok":true}`, nil
}

// -- list_channels ---------------------------------------------------

// ListChannelsTool enumerates channels the bot can see.
type ListChannelsTool struct{ c *Client }

// NewListChannelsTool constructs a ListChannelsTool.
func NewListChannelsTool(c *Client) *ListChannelsTool { return &ListChannelsTool{c: c} }

// Name implements tools.Tool.
func (*ListChannelsTool) Name() string { return "slack_list_channels" }

// Description implements tools.Tool.
func (*ListChannelsTool) Description() string {
	return "List Slack channels the bot can see. Optional: types (comma-separated: public_channel,private_channel,mpim,im), limit."
}

// InputSchema implements tools.Tool.
func (*ListChannelsTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"types": map[string]any{"type": "string"},
			"limit": map[string]any{"type": "integer"},
		},
	}
}

// Execute implements tools.Tool.
func (t *ListChannelsTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Types string `json:"types"`
		Limit int    `json:"limit"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if args.Types == "" {
		args.Types = "public_channel"
	}
	if args.Limit == 0 {
		args.Limit = 100
	}
	q := url.Values{}
	q.Set("types", args.Types)
	q.Set("limit", fmt.Sprintf("%d", args.Limit))
	var out any
	if err := t.c.get(ctx, "conversations.list", q, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// Register wires every Slack tool into reg.
func Register(reg *tools.Registry, c *Client) error {
	for _, t := range []tools.Tool{
		NewPostMessageTool(c),
		NewGetThreadTool(c),
		NewAddReactionTool(c),
		NewListChannelsTool(c),
	} {
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("slack: register %s: %w", t.Name(), err)
		}
	}
	return nil
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(b), nil
}
