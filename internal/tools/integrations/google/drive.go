package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// DriveSearchTool searches for files matching a query.
type DriveSearchTool struct{ c *Client }

// NewDriveSearchTool constructs a DriveSearchTool.
func NewDriveSearchTool(c *Client) *DriveSearchTool { return &DriveSearchTool{c: c} }

// Name implements tools.Tool.
func (*DriveSearchTool) Name() string { return "drive_search" }

// Description implements tools.Tool.
func (*DriveSearchTool) Description() string {
	return "Search Google Drive for files. Query is Drive query syntax (e.g. \"name contains 'foo' and mimeType='application/pdf'\")."
}

// InputSchema implements tools.Tool.
func (*DriveSearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"q":         map[string]any{"type": "string"},
			"page_size": map[string]any{"type": "integer"},
		},
		"required": []string{"q"},
	}
}

// Execute implements tools.Tool.
func (t *DriveSearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Q        string `json:"q"`
		PageSize int    `json:"page_size"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Q == "" {
		return "", fmt.Errorf("q is required")
	}
	if args.PageSize == 0 {
		args.PageSize = 30
	}
	q := url.Values{}
	q.Set("q", args.Q)
	q.Set("pageSize", fmt.Sprintf("%d", args.PageSize))
	q.Set("fields", "files(id,name,mimeType,modifiedTime,webViewLink)")
	u := t.c.driveBase + "/files?" + q.Encode()
	var out any
	if err := t.c.do(ctx, "GET", u, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// DriveGetTool fetches a file's metadata.
type DriveGetTool struct{ c *Client }

// NewDriveGetTool constructs a DriveGetTool.
func NewDriveGetTool(c *Client) *DriveGetTool { return &DriveGetTool{c: c} }

// Name implements tools.Tool.
func (*DriveGetTool) Name() string { return "drive_get" }

// Description implements tools.Tool.
func (*DriveGetTool) Description() string {
	return "Fetch Drive file metadata by id."
}

// InputSchema implements tools.Tool.
func (*DriveGetTool) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"required":   []string{"id"},
	}
}

// Execute implements tools.Tool.
func (t *DriveGetTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct{ ID string }
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.ID == "" {
		return "", fmt.Errorf("id is required")
	}
	q := url.Values{}
	q.Set("fields", "id,name,mimeType,modifiedTime,webViewLink,size")
	u := t.c.driveBase + "/files/" + url.PathEscape(args.ID) + "?" + q.Encode()
	var out any
	if err := t.c.do(ctx, "GET", u, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}
