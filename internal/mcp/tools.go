package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// SessionsBackend is the read-only surface tool handlers use to serve
// data. Kept narrow so tests can inject a fake without touching a real
// SQLite database.
type SessionsBackend interface {
	Search(ctx context.Context, query string, opts sqlitestore.SearchOptions) ([]sqlitestore.SearchHit, error)
	List(ctx context.Context, limit int) ([]state.Summary, error)
	Load(ctx context.Context, id string) (*agent.Session, error)
	CronList(ctx context.Context) ([]sqlitestore.CronJob, error)
}

// storeBackend adapts a *sqlitestore.Store + *sqlitestore.CronStore to
// SessionsBackend. Kept internal so callers only see the interface.
type storeBackend struct {
	sessions *sqlitestore.Store
	cron     *sqlitestore.CronStore
}

// NewStoreBackend builds a SessionsBackend from an already-open Store.
// The cron accessor is optional — if nil, the cron_list tool returns
// an empty list rather than erroring.
func NewStoreBackend(s *sqlitestore.Store, c *sqlitestore.CronStore) SessionsBackend {
	return &storeBackend{sessions: s, cron: c}
}

// Search satisfies SessionsBackend.
func (b *storeBackend) Search(ctx context.Context, query string, opts sqlitestore.SearchOptions) ([]sqlitestore.SearchHit, error) {
	return b.sessions.Search(ctx, query, opts)
}

// List satisfies SessionsBackend.
func (b *storeBackend) List(ctx context.Context, limit int) ([]state.Summary, error) {
	return b.sessions.List(ctx, limit)
}

// Load satisfies SessionsBackend.
func (b *storeBackend) Load(ctx context.Context, id string) (*agent.Session, error) {
	return b.sessions.Load(ctx, id)
}

// CronList satisfies SessionsBackend.
func (b *storeBackend) CronList(ctx context.Context) ([]sqlitestore.CronJob, error) {
	if b.cron == nil {
		return nil, nil
	}
	return b.cron.List(ctx)
}

// RegisterRousseauTools attaches rousseau's built-in read-only tool
// surface to a Server. Callers may register additional tools before
// or after this call.
func RegisterRousseauTools(s *Server, be SessionsBackend) {
	s.MustRegister(searchSessionsTool(be))
	s.MustRegister(listSessionsTool(be))
	s.MustRegister(readSessionTool(be))
	s.MustRegister(cronListTool(be))
}

// -- search_sessions ---------------------------------------------------

func searchSessionsTool(be SessionsBackend) ToolSpec {
	return ToolSpec{
		Name:        "rousseau_search_sessions",
		Description: "Full-text search across every recorded rousseau session. Uses SQLite FTS5 syntax (phrases in double quotes, AND/OR/NOT, prefix wildcards).",
		InputSchema: mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "FTS5 query"},
				"limit": map[string]any{"type": "integer", "description": "Cap hits returned. Default 20."},
			},
			"required": []string{"query"},
		}),
		Handler: func(ctx context.Context, args json.RawMessage) ([]Content, error) {
			var in struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("parse args: %w", err)
			}
			if strings.TrimSpace(in.Query) == "" {
				return nil, errors.New("query is required")
			}
			hits, err := be.Search(ctx, in.Query, sqlitestore.SearchOptions{Limit: in.Limit})
			if err != nil {
				return nil, err
			}
			if len(hits) == 0 {
				return TextContent("(no matches)"), nil
			}
			var sb strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&sb, "session %s (rank %.2f)\n    title:   %s\n    snippet: %s\n\n",
					h.SessionID, h.Rank, h.Title, h.Snippet)
			}
			return TextContent(strings.TrimSpace(sb.String())), nil
		},
	}
}

// -- list_sessions -----------------------------------------------------

func listSessionsTool(be SessionsBackend) ToolSpec {
	return ToolSpec{
		Name:        "rousseau_list_sessions",
		Description: "List rousseau sessions newest-first.",
		InputSchema: mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "description": "Cap rows returned. Default 20."},
			},
		}),
		Handler: func(ctx context.Context, args json.RawMessage) ([]Content, error) {
			var in struct {
				Limit int `json:"limit"`
			}
			_ = json.Unmarshal(args, &in) // arguments are optional here
			hits, err := be.List(ctx, in.Limit)
			if err != nil {
				return nil, err
			}
			if len(hits) == 0 {
				return TextContent("(no sessions)"), nil
			}
			var sb strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&sb, "%s  %s  msgs=%d  updated=%s\n", h.ID, h.Title, h.MessageCount, h.UpdatedAt)
			}
			return TextContent(strings.TrimSpace(sb.String())), nil
		},
	}
}

// -- read_session ------------------------------------------------------

func readSessionTool(be SessionsBackend) ToolSpec {
	return ToolSpec{
		Name:        "rousseau_read_session",
		Description: "Return the full transcript of a rousseau session by id.",
		InputSchema: mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Session id"},
			},
			"required": []string{"id"},
		}),
		Handler: func(ctx context.Context, args json.RawMessage) ([]Content, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, err
			}
			if in.ID == "" {
				return nil, errors.New("id is required")
			}
			s, err := be.Load(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "id: %s\ntitle: %s\ncreated: %s\nupdated: %s\nmessages: %d\n\n",
				s.ID, s.Title, s.CreatedAt, s.UpdatedAt, len(s.Messages))
			for i, m := range s.Messages {
				fmt.Fprintf(&sb, "[%d] %s\n", i, m.Role)
				for _, c := range m.Content {
					if c.Text != "" {
						fmt.Fprintf(&sb, "    %s\n", c.Text)
					}
				}
			}
			return TextContent(sb.String()), nil
		},
	}
}

// -- cron_list ---------------------------------------------------------

func cronListTool(be SessionsBackend) ToolSpec {
	return ToolSpec{
		Name:        "rousseau_cron_list",
		Description: "List rousseau's scheduled cron jobs (name, schedule, prompt, delivery target).",
		InputSchema: mustSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		Handler: func(ctx context.Context, _ json.RawMessage) ([]Content, error) {
			jobs, err := be.CronList(ctx)
			if err != nil {
				return nil, err
			}
			if len(jobs) == 0 {
				return TextContent("(no jobs)"), nil
			}
			var sb strings.Builder
			for _, j := range jobs {
				status := "on"
				if !j.Enabled {
					status = "off"
				}
				fmt.Fprintf(&sb, "%s [%s] %s → %s  prompt=%q  deliver=%s\n",
					j.Name, status, j.CronExpr, j.DeliverTo, j.Prompt, j.DeliverTo)
			}
			return TextContent(strings.TrimSpace(sb.String())), nil
		},
	}
}

// mustSchema converts a JSON-Schema map to json.RawMessage. Panics on
// programmer error; the schemas here are hand-written and safe.
func mustSchema(v map[string]any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
