package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// CalendarListEventsTool lists events on a calendar within a window.
type CalendarListEventsTool struct{ c *Client }

// NewCalendarListEventsTool constructs a CalendarListEventsTool.
func NewCalendarListEventsTool(c *Client) *CalendarListEventsTool {
	return &CalendarListEventsTool{c: c}
}

// Name implements tools.Tool.
func (*CalendarListEventsTool) Name() string { return "calendar_list_events" }

// Description implements tools.Tool.
func (*CalendarListEventsTool) Description() string {
	return "List Google Calendar events. Optional: calendar_id (default 'primary'), time_min / time_max in RFC3339, max_results."
}

// InputSchema implements tools.Tool.
func (*CalendarListEventsTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"calendar_id": map[string]any{"type": "string"},
			"time_min":    map[string]any{"type": "string"},
			"time_max":    map[string]any{"type": "string"},
			"max_results": map[string]any{"type": "integer"},
		},
	}
}

// Execute implements tools.Tool.
func (t *CalendarListEventsTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		CalendarID string `json:"calendar_id"`
		TimeMin    string `json:"time_min"`
		TimeMax    string `json:"time_max"`
		MaxResults int    `json:"max_results"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if args.CalendarID == "" {
		args.CalendarID = "primary"
	}
	if args.MaxResults == 0 {
		args.MaxResults = 30
	}
	q := url.Values{}
	q.Set("maxResults", fmt.Sprintf("%d", args.MaxResults))
	q.Set("singleEvents", "true")
	q.Set("orderBy", "startTime")
	if args.TimeMin != "" {
		q.Set("timeMin", args.TimeMin)
	} else {
		q.Set("timeMin", time.Now().UTC().Format(time.RFC3339))
	}
	if args.TimeMax != "" {
		q.Set("timeMax", args.TimeMax)
	}
	u := t.c.calendarBase + "/calendars/" + url.PathEscape(args.CalendarID) + "/events?" + q.Encode()
	var out any
	if err := t.c.do(ctx, "GET", u, nil, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}

// CalendarCreateEventTool creates an event.
type CalendarCreateEventTool struct{ c *Client }

// NewCalendarCreateEventTool constructs a CalendarCreateEventTool.
func NewCalendarCreateEventTool(c *Client) *CalendarCreateEventTool {
	return &CalendarCreateEventTool{c: c}
}

// Name implements tools.Tool.
func (*CalendarCreateEventTool) Name() string { return "calendar_create_event" }

// Description implements tools.Tool.
func (*CalendarCreateEventTool) Description() string {
	return "Create a Google Calendar event. Required: summary, start, end (RFC3339). Optional: calendar_id, description, attendees (array of emails)."
}

// InputSchema implements tools.Tool.
func (*CalendarCreateEventTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"calendar_id": map[string]any{"type": "string"},
			"summary":     map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"start":       map[string]any{"type": "string"},
			"end":         map[string]any{"type": "string"},
			"attendees":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"summary", "start", "end"},
	}
}

// Execute implements tools.Tool.
func (t *CalendarCreateEventTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		CalendarID  string   `json:"calendar_id"`
		Summary     string   `json:"summary"`
		Description string   `json:"description"`
		Start       string   `json:"start"`
		End         string   `json:"end"`
		Attendees   []string `json:"attendees"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if args.Summary == "" || args.Start == "" || args.End == "" {
		return "", fmt.Errorf("summary, start and end are required")
	}
	if args.CalendarID == "" {
		args.CalendarID = "primary"
	}
	event := map[string]any{
		"summary": args.Summary,
		"start":   map[string]any{"dateTime": args.Start},
		"end":     map[string]any{"dateTime": args.End},
	}
	if args.Description != "" {
		event["description"] = args.Description
	}
	if len(args.Attendees) > 0 {
		attendees := make([]map[string]any, len(args.Attendees))
		for i, a := range args.Attendees {
			attendees[i] = map[string]any{"email": a}
		}
		event["attendees"] = attendees
	}
	u := t.c.calendarBase + "/calendars/" + url.PathEscape(args.CalendarID) + "/events"
	var out any
	if err := t.c.do(ctx, "POST", u, event, &out); err != nil {
		return "", err
	}
	return jsonString(out)
}
