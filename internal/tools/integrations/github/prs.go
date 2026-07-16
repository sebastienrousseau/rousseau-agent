package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// ListPRsTool lists pull requests on a repo.
type ListPRsTool struct{ c *Client }

// NewListPRsTool constructs a ListPRsTool.
func NewListPRsTool(c *Client) *ListPRsTool { return &ListPRsTool{c: c} }

// Name implements tools.Tool.
func (*ListPRsTool) Name() string { return "github_list_prs" }

// Description implements tools.Tool.
func (*ListPRsTool) Description() string {
	return "List pull requests on a GitHub repository. Filters: state (open|closed|all)."
}

// InputSchema implements tools.Tool.
func (*ListPRsTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"owner": map[string]any{"type": "string"},
			"repo":  map[string]any{"type": "string"},
			"state": map[string]any{
				"type": "string",
				"enum": []string{"open", "closed", "all"},
			},
			"per_page": map[string]any{"type": "integer"},
		},
		"required": []string{"owner", "repo"},
	}
}

// Execute implements tools.Tool.
func (t *ListPRsTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Owner, Repo, State string
		PerPage            int `json:"per_page"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Owner == "" || args.Repo == "" {
		return "", fmt.Errorf("owner and repo are required")
	}
	if args.State == "" {
		args.State = "open"
	}
	if args.PerPage == 0 {
		args.PerPage = 30
	}
	q := url.Values{}
	q.Set("state", args.State)
	q.Set("per_page", fmt.Sprintf("%d", args.PerPage))
	path := fmt.Sprintf("/repos/%s/%s/pulls?%s", url.PathEscape(args.Owner), url.PathEscape(args.Repo), q.Encode())
	var out []struct {
		Number    int                    `json:"number"`
		Title     string                 `json:"title"`
		State     string                 `json:"state"`
		HTMLURL   string                 `json:"html_url"`
		User      struct{ Login string } `json:"user"`
		CreatedAt string                 `json:"created_at"`
		Merged    bool                   `json:"merged"`
		Draft     bool                   `json:"draft"`
	}
	if err := t.c.do(ctx, "GET", path, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// GetPRTool fetches a single PR's metadata.
type GetPRTool struct{ c *Client }

// NewGetPRTool constructs a GetPRTool.
func NewGetPRTool(c *Client) *GetPRTool { return &GetPRTool{c: c} }

// Name implements tools.Tool.
func (*GetPRTool) Name() string { return "github_get_pr" }

// Description implements tools.Tool.
func (*GetPRTool) Description() string {
	return "Fetch a single pull request by number."
}

// InputSchema implements tools.Tool.
func (*GetPRTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"owner":  map[string]any{"type": "string"},
			"repo":   map[string]any{"type": "string"},
			"number": map[string]any{"type": "integer"},
		},
		"required": []string{"owner", "repo", "number"},
	}
}

// Execute implements tools.Tool.
func (t *GetPRTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Owner, Repo string
		Number      int `json:"number"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Owner == "" || args.Repo == "" || args.Number == 0 {
		return "", fmt.Errorf("owner, repo and number are required")
	}
	var out any
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(args.Owner), url.PathEscape(args.Repo), args.Number)
	if err := t.c.do(ctx, "GET", path, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}
