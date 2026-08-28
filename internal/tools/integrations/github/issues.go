package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// ListIssuesTool lists issues on a repo.
type ListIssuesTool struct{ c *Client }

// NewListIssuesTool constructs a ListIssuesTool.
func NewListIssuesTool(c *Client) *ListIssuesTool { return &ListIssuesTool{c: c} }

// Name implements tools.Tool.
func (*ListIssuesTool) Name() string { return "github_list_issues" }

// Description implements tools.Tool.
func (*ListIssuesTool) Description() string {
	return "List issues on a GitHub repository. Filters: state (open|closed|all), labels (comma-separated)."
}

// InputSchema implements tools.Tool.
func (*ListIssuesTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"owner":    map[string]any{"type": "string"},
			"repo":     map[string]any{"type": "string"},
			"state":    map[string]any{"type": "string", "enum": []string{"open", "closed", "all"}},
			"labels":   map[string]any{"type": "string", "description": "Comma-separated label names."},
			"per_page": map[string]any{"type": "integer"},
		},
		"required": []string{"owner", "repo"},
	}
}

// Execute implements tools.Tool.
func (t *ListIssuesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Owner, Repo, State, Labels string
		PerPage                    int `json:"per_page"`
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
	if args.Labels != "" {
		q.Set("labels", args.Labels)
	}
	path := fmt.Sprintf("/repos/%s/%s/issues?%s", url.PathEscape(args.Owner), url.PathEscape(args.Repo), q.Encode())
	var out []struct {
		Number    int                     `json:"number"`
		Title     string                  `json:"title"`
		State     string                  `json:"state"`
		HTMLURL   string                  `json:"html_url"`
		User      struct{ Login string }  `json:"user"`
		Labels    []struct{ Name string } `json:"labels"`
		CreatedAt string                  `json:"created_at"`
	}
	if err := t.c.do(ctx, "GET", path, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// GetIssueTool fetches one issue by number.
type GetIssueTool struct{ c *Client }

// NewGetIssueTool constructs a GetIssueTool.
func NewGetIssueTool(c *Client) *GetIssueTool { return &GetIssueTool{c: c} }

// Name implements tools.Tool.
func (*GetIssueTool) Name() string { return "github_get_issue" }

// Description implements tools.Tool.
func (*GetIssueTool) Description() string {
	return "Fetch a single issue by number, including body."
}

// InputSchema implements tools.Tool.
func (*GetIssueTool) InputSchema() map[string]any {
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
func (t *GetIssueTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
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
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(args.Owner), url.PathEscape(args.Repo), args.Number)
	if err := t.c.do(ctx, "GET", path, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// CreateIssueTool opens a new issue.
type CreateIssueTool struct{ c *Client }

// NewCreateIssueTool constructs a CreateIssueTool.
func NewCreateIssueTool(c *Client) *CreateIssueTool { return &CreateIssueTool{c: c} }

// Name implements tools.Tool.
func (*CreateIssueTool) Name() string { return "github_create_issue" }

// Description implements tools.Tool.
func (*CreateIssueTool) Description() string {
	return "Create a new GitHub issue. Requires owner, repo, title. Optional: body, labels (array of strings)."
}

// InputSchema implements tools.Tool.
func (*CreateIssueTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"owner":  map[string]any{"type": "string"},
			"repo":   map[string]any{"type": "string"},
			"title":  map[string]any{"type": "string"},
			"body":   map[string]any{"type": "string"},
			"labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"owner", "repo", "title"},
	}
}

// Execute implements tools.Tool.
func (t *CreateIssueTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Owner, Repo, Title, Body string
		Labels                   []string `json:"labels"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Owner == "" || args.Repo == "" || args.Title == "" {
		return "", fmt.Errorf("owner, repo and title are required")
	}
	body := map[string]any{"title": args.Title}
	if args.Body != "" {
		body["body"] = args.Body
	}
	if len(args.Labels) > 0 {
		body["labels"] = args.Labels
	}
	var out any
	path := fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(args.Owner), url.PathEscape(args.Repo))
	if err := t.c.do(ctx, "POST", path, body, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// CommentIssueTool posts a comment on an issue or PR.
type CommentIssueTool struct{ c *Client }

// NewCommentIssueTool constructs a CommentIssueTool.
func NewCommentIssueTool(c *Client) *CommentIssueTool { return &CommentIssueTool{c: c} }

// Name implements tools.Tool.
func (*CommentIssueTool) Name() string { return "github_comment_issue" }

// Description implements tools.Tool.
func (*CommentIssueTool) Description() string {
	return "Post a comment on a GitHub issue or PR. Requires owner, repo, number, body."
}

// InputSchema implements tools.Tool.
func (*CommentIssueTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"owner":  map[string]any{"type": "string"},
			"repo":   map[string]any{"type": "string"},
			"number": map[string]any{"type": "integer"},
			"body":   map[string]any{"type": "string"},
		},
		"required": []string{"owner", "repo", "number", "body"},
	}
}

// Execute implements tools.Tool.
func (t *CommentIssueTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Owner, Repo, Body string
		Number            int `json:"number"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Owner == "" || args.Repo == "" || args.Number == 0 || args.Body == "" {
		return "", fmt.Errorf("owner, repo, number and body are required")
	}
	var out any
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(args.Owner), url.PathEscape(args.Repo), args.Number)
	if err := t.c.do(ctx, "POST", path, map[string]any{"body": args.Body}, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}
