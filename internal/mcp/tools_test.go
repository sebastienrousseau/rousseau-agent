package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

type fakeBackend struct {
	searchHits []sqlitestore.SearchHit
	searchErr  error
	summaries  []state.Summary
	session    *agent.Session
	loadErr    error
	cronJobs   []sqlitestore.CronJob
	cronErr    error
}

func (f *fakeBackend) Search(context.Context, string, sqlitestore.SearchOptions) ([]sqlitestore.SearchHit, error) {
	return f.searchHits, f.searchErr
}
func (f *fakeBackend) List(context.Context, int) ([]state.Summary, error) {
	return f.summaries, nil
}
func (f *fakeBackend) Load(_ context.Context, id string) (*agent.Session, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if f.session != nil && f.session.ID == id {
		return f.session, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeBackend) CronList(context.Context) ([]sqlitestore.CronJob, error) {
	return f.cronJobs, f.cronErr
}

func callTool(t *testing.T, spec ToolSpec, args string) ([]Content, error) {
	t.Helper()
	return spec.Handler(context.Background(), json.RawMessage(args))
}

func TestSearchSessionsTool_HappyPath(t *testing.T) {
	be := &fakeBackend{searchHits: []sqlitestore.SearchHit{
		{SessionID: "a", Title: "kubernetes", Snippet: "pod stuck…", Rank: -0.5},
	}}
	spec := searchSessionsTool(be)
	content, err := callTool(t, spec, `{"query":"pod"}`)
	require.NoError(t, err)
	require.Len(t, content, 1)
	assert.Contains(t, content[0].Text, "session a")
	assert.Contains(t, content[0].Text, "kubernetes")
}

func TestSearchSessionsTool_EmptyQuery(t *testing.T) {
	spec := searchSessionsTool(&fakeBackend{})
	_, err := callTool(t, spec, `{"query":""}`)
	assert.Error(t, err)
}

func TestSearchSessionsTool_NoHits(t *testing.T) {
	spec := searchSessionsTool(&fakeBackend{})
	content, err := callTool(t, spec, `{"query":"nothing"}`)
	require.NoError(t, err)
	assert.Equal(t, "(no matches)", content[0].Text)
}

func TestSearchSessionsTool_BackendError(t *testing.T) {
	spec := searchSessionsTool(&fakeBackend{searchErr: errors.New("db down")})
	_, err := callTool(t, spec, `{"query":"anything"}`)
	assert.Error(t, err)
}

func TestListSessionsTool(t *testing.T) {
	be := &fakeBackend{summaries: []state.Summary{
		{ID: "s1", Title: "one", MessageCount: 3, UpdatedAt: "2026-07-11"},
	}}
	spec := listSessionsTool(be)
	content, err := callTool(t, spec, `{}`)
	require.NoError(t, err)
	assert.Contains(t, content[0].Text, "s1")
	assert.Contains(t, content[0].Text, "msgs=3")
}

func TestListSessionsTool_Empty(t *testing.T) {
	spec := listSessionsTool(&fakeBackend{})
	content, err := callTool(t, spec, `{}`)
	require.NoError(t, err)
	assert.Equal(t, "(no sessions)", content[0].Text)
}

func TestReadSessionTool_HappyPath(t *testing.T) {
	sess := agent.NewSession("welcome")
	sess.Append(agent.NewUserText("hi"))
	sess.Append(agent.NewAssistantText("hello"))
	spec := readSessionTool(&fakeBackend{session: sess})
	args, _ := json.Marshal(map[string]string{"id": sess.ID})
	content, err := callTool(t, spec, string(args))
	require.NoError(t, err)
	assert.Contains(t, content[0].Text, "welcome")
	assert.Contains(t, content[0].Text, "hello")
}

func TestReadSessionTool_MissingID(t *testing.T) {
	spec := readSessionTool(&fakeBackend{})
	_, err := callTool(t, spec, `{}`)
	assert.Error(t, err)
}

func TestReadSessionTool_LoadError(t *testing.T) {
	spec := readSessionTool(&fakeBackend{loadErr: errors.New("no such")})
	_, err := callTool(t, spec, `{"id":"missing"}`)
	assert.Error(t, err)
}

func TestCronListTool(t *testing.T) {
	be := &fakeBackend{cronJobs: []sqlitestore.CronJob{
		{Name: "morning", CronExpr: "0 8 * * *", Prompt: "brief", DeliverTo: "u@x", Enabled: true, CreatedAt: time.Now()},
	}}
	spec := cronListTool(be)
	content, err := callTool(t, spec, `{}`)
	require.NoError(t, err)
	assert.Contains(t, content[0].Text, "morning")
	assert.Contains(t, content[0].Text, "on")
}

func TestCronListTool_Empty(t *testing.T) {
	spec := cronListTool(&fakeBackend{})
	content, err := callTool(t, spec, `{}`)
	require.NoError(t, err)
	assert.Equal(t, "(no jobs)", content[0].Text)
}

func TestRegisterRousseauTools(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	RegisterRousseauTools(s, &fakeBackend{})
	// Round-trip tools/list should return four tools.
	resp := call(t, s, MethodToolsList, json.RawMessage(`1`), nil)
	require.Nil(t, resp.Error)
	var r ToolsListResult
	require.NoError(t, json.Unmarshal(resp.Result, &r))
	assert.Len(t, r.Tools, 4)
}
