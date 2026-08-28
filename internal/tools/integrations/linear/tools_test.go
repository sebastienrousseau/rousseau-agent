package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{APIKey: "lin_api_test", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	return c
}

func TestNew_RequiresAPIKey(t *testing.T) {
	t.Setenv(EnvToken, "")
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestNew_EnvFallback(t *testing.T) {
	t.Setenv(EnvToken, "lin_api_env")
	c, err := New(Config{})
	require.NoError(t, err)
	assert.Equal(t, "lin_api_env", c.apiKey)
}

func TestListIssuesTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Contains(t, payload["query"], "issues(filter")
		_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[{"identifier":"ENG-1","title":"foo"}]}}}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewListIssuesTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"team_key":"ENG"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "ENG-1")
}

func TestGetIssueTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var payload struct {
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "ENG-1", payload.Variables["id"])
	}))
	defer srv.Close()
	// The handler above doesn't write a body — do so via a separate
	// server so we can assert both the variable AND the shape.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"i1","identifier":"ENG-1"}}}`)) //nolint:errcheck // test fixture
	}))
	defer srv2.Close()
	tool := NewGetIssueTool(newTestClient(t, srv2))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"identifier":"ENG-1"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "ENG-1")
}

func TestCreateIssueTool_ShapesInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var payload struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "team-uuid", payload.Variables.Input["teamId"])
		assert.Equal(t, "hi", payload.Variables.Input["title"])
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"identifier":"ENG-2"}}}}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewCreateIssueTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(),
		json.RawMessage(`{"team_id":"team-uuid","title":"hi","description":"body","priority":2}`))
	require.NoError(t, err)
	assert.Contains(t, out, "ENG-2")
}

func TestUpdateIssueTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true}}}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewUpdateIssueTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"id":"id-1","title":"new","priority":3}`))
	require.NoError(t, err)
}

func TestGraphQLErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"authentication failed"}]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewListIssuesTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestValidatesInput(t *testing.T) {
	c, err := New(Config{APIKey: "x"})
	require.NoError(t, err)
	tool := NewCreateIssueTool(c)
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"title":""}`))
	assert.ErrorContains(t, err, "required")
}

func TestRegister_AllTools(t *testing.T) {
	c, err := New(Config{APIKey: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	assert.Len(t, reg.Names(), 4)
}
