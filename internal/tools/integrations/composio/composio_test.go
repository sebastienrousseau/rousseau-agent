package composio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

const listBodyTwo = `{"items":[{"name":"GMAIL_SEND","appKey":"gmail","description":"send","parameters":{"type":"object","properties":{"to":{"type":"string"}}}},{"name":"SLACK_POST","appKey":"slack","description":"post"}]}`

const listBodyThree = `{"items":[{"name":"GMAIL_SEND","appKey":"gmail","description":"send","parameters":{"type":"object"}},{"name":"SLACK_POST","appKey":"slack","description":"post","parameters":{"type":"object"}},{"name":"GITHUB_STAR","appKey":"github","description":"star","parameters":{"type":"object"}}]}`

const listBodyFilter = `{"items":[{"name":"GMAIL_SEND","appKey":"gmail","parameters":{"type":"object"}},{"name":"SLACK_POST","appKey":"slack","parameters":{"type":"object"}}]}`

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{
		APIKey: "test-key", UserID: "user-1",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	})
	require.NoError(t, err)
	return c
}

// writeBody is a tiny helper so the //nolint:errcheck directive lives
// next to the actual w.Write call, not on the multi-line RawMessage
// literal above it (which confuses the nolintlint linter).
func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	_, err := w.Write([]byte(body))
	require.NoError(t, err)
}

func TestNew_RequiresAPIKey(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvUserID, "u")
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestNew_RequiresUserID(t *testing.T) {
	t.Setenv(EnvAPIKey, "k")
	t.Setenv(EnvUserID, "")
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestNew_EnvFallback(t *testing.T) {
	t.Setenv(EnvAPIKey, "env-k")
	t.Setenv(EnvUserID, "env-u")
	c, err := New(Config{})
	require.NoError(t, err)
	assert.Equal(t, "env-k", c.apiKey)
	assert.Equal(t, "env-u", c.userID)
}

func TestList_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/actions", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		writeBody(t, w, listBodyTwo)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	actions, err := c.List(context.Background())
	require.NoError(t, err)
	require.Len(t, actions, 2)
	assert.Equal(t, "GMAIL_SEND", actions[0].Name)
	assert.Equal(t, "gmail", actions[0].AppKey)
}

func TestExecute_SendsUserIDAndActionName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/actions/execute", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "user-1", payload["userId"])
		assert.Equal(t, "GMAIL_SEND", payload["actionName"])
		assert.NotNil(t, payload["input"])
		writeBody(t, w, `{"messageId":"abc"}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	out, err := c.Execute(context.Background(), "GMAIL_SEND", json.RawMessage(`{"to":"a@b"}`))
	require.NoError(t, err)
	assert.Contains(t, string(out), "messageId")
}

func TestExecute_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Execute(context.Background(), "GMAIL_SEND", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestRegister_RegistersEachAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, listBodyThree)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	reg := tools.NewRegistry()
	n, err := Register(context.Background(), reg, c, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Len(t, reg.Names(), 3)
	for _, name := range reg.Names() {
		assert.True(t, strings.HasPrefix(name, "cx_"), "unexpected name %q", name)
	}
}

func TestRegister_AppFilterNarrows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, listBodyFilter)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	reg := tools.NewRegistry()
	n, err := Register(context.Background(), reg, c, []string{"gmail"})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Len(t, reg.Names(), 1)
}

func TestAction_InputSchemaPassthrough(t *testing.T) {
	a := &action{
		spec: Action{
			Parameters: json.RawMessage(`{"type":"object","required":["to"]}`),
		},
		toolID: "cx_gmail_send",
	}
	schema := a.InputSchema()
	assert.Equal(t, "object", schema["type"])
	assert.NotNil(t, schema["required"])
}

func TestAction_InputSchemaHandlesEmpty(t *testing.T) {
	a := &action{spec: Action{}}
	assert.Equal(t, "object", a.InputSchema()["type"])
}

func TestAction_DescriptionFallback(t *testing.T) {
	a := &action{spec: Action{Name: "GMAIL_SEND", AppKey: "gmail"}}
	assert.Contains(t, a.Description(), "GMAIL_SEND")
}

func TestToolIDIsLowerSnake(t *testing.T) {
	assert.Equal(t, "cx_gmail_gmail_send", toToolID("gmail", "GMAIL_SEND"))
	assert.Equal(t, "cx_slack_post_message", toToolID("slack", "post_message"))
	assert.Equal(t, "cx_app_thing_foo_bar", toToolID("app-thing", "foo bar"))
}
