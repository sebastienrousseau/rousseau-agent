package linear

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// -- list_issues -----------------------------------------------------

// ListIssuesTool lists issues, optionally filtered by team or state.
type ListIssuesTool struct{ c *Client }

// NewListIssuesTool constructs a ListIssuesTool.
func NewListIssuesTool(c *Client) *ListIssuesTool { return &ListIssuesTool{c: c} }

// Name implements tools.Tool.
func (*ListIssuesTool) Name() string { return "linear_list_issues" }

// Description implements tools.Tool.
func (*ListIssuesTool) Description() string {
	return "List Linear issues. Optional filters: team_key (e.g. ENG), state (e.g. In Progress), first (default 25)."
}

// InputSchema implements tools.Tool.
func (*ListIssuesTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"team_key": map[string]any{"type": "string"},
			"state":    map[string]any{"type": "string"},
			"first":    map[string]any{"type": "integer"},
		},
	}
}

// Execute implements tools.Tool.
func (t *ListIssuesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		TeamKey string `json:"team_key"`
		State   string `json:"state"`
		First   int    `json:"first"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if args.First == 0 {
		args.First = 25
	}
	filter := map[string]any{}
	if args.TeamKey != "" {
		filter["team"] = map[string]any{"key": map[string]any{"eq": args.TeamKey}}
	}
	if args.State != "" {
		filter["state"] = map[string]any{"name": map[string]any{"eq": args.State}}
	}

	q := `query($filter: IssueFilter, $first: Int!) {
		issues(filter: $filter, first: $first) {
			nodes { id identifier title state { name } assignee { name } url }
		}
	}`
	var out any
	err := t.c.query(ctx, q, map[string]any{"filter": filter, "first": args.First}, &out)
	if err != nil {
		return "", err
	}
	return jsonString(out)
}

// -- get_issue -------------------------------------------------------

// GetIssueTool fetches a single issue by identifier ("ENG-123").
type GetIssueTool struct{ c *Client }

// NewGetIssueTool constructs a GetIssueTool.
func NewGetIssueTool(c *Client) *GetIssueTool { return &GetIssueTool{c: c} }

// Name implements tools.Tool.
func (*GetIssueTool) Name() string { return "linear_get_issue" }

// Description implements tools.Tool.
func (*GetIssueTool) Description() string {
	return "Fetch one Linear issue by identifier (e.g. ENG-123)."
}

// InputSchema implements tools.Tool.
func (*GetIssueTool) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"identifier": map[string]any{"type": "string"}},
		"required":   []string{"identifier"},
	}
}

// Execute implements tools.Tool.
func (t *GetIssueTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct{ Identifier string }
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Identifier == "" {
		return "", fmt.Errorf("identifier is required")
	}
	q := `query($id: String!) {
		issue(id: $id) {
			id identifier title description state { name } assignee { name } url
			team { key name }
		}
	}`
	var out any
	if err := t.c.query(ctx, q, map[string]any{"id": args.Identifier}, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// -- create_issue ----------------------------------------------------

// CreateIssueTool opens a new issue.
type CreateIssueTool struct{ c *Client }

// NewCreateIssueTool constructs a CreateIssueTool.
func NewCreateIssueTool(c *Client) *CreateIssueTool { return &CreateIssueTool{c: c} }

// Name implements tools.Tool.
func (*CreateIssueTool) Name() string { return "linear_create_issue" }

// Description implements tools.Tool.
func (*CreateIssueTool) Description() string {
	return "Create a Linear issue. Required: team_id, title. Optional: description, priority (0-4)."
}

// InputSchema implements tools.Tool.
func (*CreateIssueTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"team_id":     map[string]any{"type": "string"},
			"title":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"priority":    map[string]any{"type": "integer"},
		},
		"required": []string{"team_id", "title"},
	}
}

// Execute implements tools.Tool.
func (t *CreateIssueTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		TeamID      string `json:"team_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    int    `json:"priority"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.TeamID == "" || args.Title == "" {
		return "", fmt.Errorf("team_id and title are required")
	}
	inputVar := map[string]any{"teamId": args.TeamID, "title": args.Title}
	if args.Description != "" {
		inputVar["description"] = args.Description
	}
	if args.Priority > 0 {
		inputVar["priority"] = args.Priority
	}
	m := `mutation($input: IssueCreateInput!) {
		issueCreate(input: $input) {
			success
			issue { id identifier title url }
		}
	}`
	var out any
	if err := t.c.query(ctx, m, map[string]any{"input": inputVar}, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// -- update_issue ----------------------------------------------------

// UpdateIssueTool patches an existing issue.
type UpdateIssueTool struct{ c *Client }

// NewUpdateIssueTool constructs an UpdateIssueTool.
func NewUpdateIssueTool(c *Client) *UpdateIssueTool { return &UpdateIssueTool{c: c} }

// Name implements tools.Tool.
func (*UpdateIssueTool) Name() string { return "linear_update_issue" }

// Description implements tools.Tool.
func (*UpdateIssueTool) Description() string {
	return "Update fields on a Linear issue by id. Any of title, description, state_id, priority may be supplied."
}

// InputSchema implements tools.Tool.
func (*UpdateIssueTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "string"},
			"title":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"state_id":    map[string]any{"type": "string"},
			"priority":    map[string]any{"type": "integer"},
		},
		"required": []string{"id"},
	}
}

// Execute implements tools.Tool.
func (t *UpdateIssueTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		StateID     string `json:"state_id"`
		Priority    int    `json:"priority"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.ID == "" {
		return "", fmt.Errorf("id is required")
	}
	inputVar := map[string]any{}
	if args.Title != "" {
		inputVar["title"] = args.Title
	}
	if args.Description != "" {
		inputVar["description"] = args.Description
	}
	if args.StateID != "" {
		inputVar["stateId"] = args.StateID
	}
	if args.Priority > 0 {
		inputVar["priority"] = args.Priority
	}
	m := `mutation($id: String!, $input: IssueUpdateInput!) {
		issueUpdate(id: $id, input: $input) {
			success
			issue { id identifier title state { name } url }
		}
	}`
	var out any
	if err := t.c.query(ctx, m, map[string]any{"id": args.ID, "input": inputVar}, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// Register wires every Linear tool into reg.
func Register(reg *tools.Registry, c *Client) error {
	for _, t := range []tools.Tool{
		NewListIssuesTool(c),
		NewGetIssueTool(c),
		NewCreateIssueTool(c),
		NewUpdateIssueTool(c),
	} {
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("linear: register %s: %w", t.Name(), err)
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
