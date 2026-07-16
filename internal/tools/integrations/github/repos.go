package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// ListReposTool returns repositories owned by, or accessible to, the
// authenticated user. Model input: {"visibility": "all|public|private"}.
type ListReposTool struct{ c *Client }

// NewListReposTool constructs a ListReposTool.
func NewListReposTool(c *Client) *ListReposTool { return &ListReposTool{c: c} }

// Name implements tools.Tool.
func (*ListReposTool) Name() string { return "github_list_repos" }

// Description implements tools.Tool.
func (*ListReposTool) Description() string {
	return "List GitHub repositories accessible to the authenticated user. Optional filter: visibility (all|public|private)."
}

// InputSchema implements tools.Tool.
func (*ListReposTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"visibility": map[string]any{
				"type":        "string",
				"description": "One of 'all', 'public', 'private'. Default 'all'.",
				"enum":        []string{"all", "public", "private"},
			},
			"per_page": map[string]any{
				"type":        "integer",
				"description": "Number of results per page (max 100). Default 30.",
			},
		},
	}
}

// Execute implements tools.Tool.
func (t *ListReposTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Visibility string `json:"visibility"`
		PerPage    int    `json:"per_page"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if args.Visibility == "" {
		args.Visibility = "all"
	}
	if args.PerPage == 0 {
		args.PerPage = 30
	}
	q := url.Values{}
	q.Set("visibility", args.Visibility)
	q.Set("per_page", fmt.Sprintf("%d", args.PerPage))
	var repos []struct {
		FullName    string `json:"full_name"`
		Private     bool   `json:"private"`
		Description string `json:"description"`
		HTMLURL     string `json:"html_url"`
		Language    string `json:"language"`
		Stargazers  int    `json:"stargazers_count"`
		UpdatedAt   string `json:"updated_at"`
	}
	if err := t.c.do(ctx, "GET", "/user/repos?"+q.Encode(), nil, &repos); err != nil {
		return "", err
	}
	return jsonString(repos)
}

// GetRepoTool returns metadata for one repo by owner/repo.
type GetRepoTool struct{ c *Client }

// NewGetRepoTool constructs a GetRepoTool.
func NewGetRepoTool(c *Client) *GetRepoTool { return &GetRepoTool{c: c} }

// Name implements tools.Tool.
func (*GetRepoTool) Name() string { return "github_get_repo" }

// Description implements tools.Tool.
func (*GetRepoTool) Description() string {
	return "Fetch metadata for one GitHub repository. Input: owner and repo names."
}

// InputSchema implements tools.Tool.
func (*GetRepoTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"owner": map[string]any{"type": "string"},
			"repo":  map[string]any{"type": "string"},
		},
		"required": []string{"owner", "repo"},
	}
}

// Execute implements tools.Tool.
func (t *GetRepoTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct{ Owner, Repo string }
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Owner == "" || args.Repo == "" {
		return "", fmt.Errorf("owner and repo are required")
	}
	var out any
	path := fmt.Sprintf("/repos/%s/%s", url.PathEscape(args.Owner), url.PathEscape(args.Repo))
	if err := t.c.do(ctx, "GET", path, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// SearchCodeTool runs GitHub's code search.
type SearchCodeTool struct{ c *Client }

// NewSearchCodeTool constructs a SearchCodeTool.
func NewSearchCodeTool(c *Client) *SearchCodeTool { return &SearchCodeTool{c: c} }

// Name implements tools.Tool.
func (*SearchCodeTool) Name() string { return "github_search_code" }

// Description implements tools.Tool.
func (*SearchCodeTool) Description() string {
	return "Search for code across GitHub. Input: a search query using GitHub's code-search syntax (e.g. 'repo:owner/repo path:src/ func handler')."
}

// InputSchema implements tools.Tool.
func (*SearchCodeTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"q":        map[string]any{"type": "string", "description": "GitHub code-search query."},
			"per_page": map[string]any{"type": "integer"},
		},
		"required": []string{"q"},
	}
}

// Execute implements tools.Tool.
func (t *SearchCodeTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Q       string `json:"q"`
		PerPage int    `json:"per_page"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Q == "" {
		return "", fmt.Errorf("q is required")
	}
	if args.PerPage == 0 {
		args.PerPage = 20
	}
	q := url.Values{}
	q.Set("q", args.Q)
	q.Set("per_page", fmt.Sprintf("%d", args.PerPage))
	var out any
	if err := t.c.do(ctx, "GET", "/search/code?"+q.Encode(), nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// jsonString marshals v to a compact JSON string.
func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(b), nil
}
