package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// TestEveryToolExposesMetadata walks every registered tool and hits
// Name / Description / InputSchema. Catches accidental empty
// descriptions or malformed schemas that the model would refuse.
func TestEveryToolExposesMetadata(t *testing.T) {
	c, err := New(Config{Token: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	for _, name := range reg.Names() {
		tool, ok := reg.Get(name)
		require.True(t, ok)
		assert.NotEmpty(t, tool.Name())
		assert.NotEmpty(t, tool.Description())
		schema := tool.InputSchema()
		assert.Equal(t, "object", schema["type"])
	}
}

func TestListIssuesTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/repos/a/b/issues")
		assert.Contains(t, r.URL.RawQuery, "state=open")
		_, _ = w.Write([]byte(`[{"number":1,"title":"a bug"}]`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{Token: "x", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewListIssuesTool(c)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "a bug")
}

func TestListIssuesTool_LabelFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "labels=bug")
		_, _ = w.Write([]byte(`[]`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{Token: "x", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewListIssuesTool(c)
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b","labels":"bug"}`))
	require.NoError(t, err)
}

func TestGetIssueTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/a/b/issues/42", r.URL.Path)
		_, _ = w.Write([]byte(`{"number":42,"body":"reprosteps"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{Token: "x", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewGetIssueTool(c)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b","number":42}`))
	require.NoError(t, err)
	assert.Contains(t, out, "reprosteps")
}

func TestGetIssueTool_ValidatesInput(t *testing.T) {
	tool := NewGetIssueTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"a"}`))
	assert.ErrorContains(t, err, "required")
}

func TestCreateIssueTool_ValidatesInput(t *testing.T) {
	tool := NewCreateIssueTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b"}`))
	assert.ErrorContains(t, err, "required")
}

func TestCommentIssueTool_ValidatesInput(t *testing.T) {
	tool := NewCommentIssueTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b","number":1}`))
	assert.ErrorContains(t, err, "required")
}

func TestListPRsTool_ValidatesInput(t *testing.T) {
	tool := NewListPRsTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":""}`))
	assert.ErrorContains(t, err, "required")
}

func TestGetPRTool_ValidatesInput(t *testing.T) {
	tool := NewGetPRTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.ErrorContains(t, err, "required")
}

func TestListReposTool_HandlesEmptyInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "visibility=all")
		_, _ = w.Write([]byte(`[]`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{Token: "x", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewListReposTool(c)
	_, err = tool.Execute(context.Background(), nil)
	require.NoError(t, err)
}

func TestFirstNonEmpty(t *testing.T) {
	assert.Equal(t, "b", firstNonEmpty("", "b", "c"))
	assert.Equal(t, "a", firstNonEmpty("a", "b"))
	assert.Empty(t, firstNonEmpty("", ""))
}

func TestClient_BadJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{Token: "x", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewGetRepoTool(c)
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b"}`))
	assert.ErrorContains(t, err, "decode")
}

// TestUserAgentHeader confirms the client sends its identifier so
// GitHub's rate-limiter can attribute quota correctly.
func TestUserAgentHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("User-Agent"))
		assert.Contains(t, r.Header.Get("User-Agent"), "rousseau")
		_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{Token: "x", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewGetRepoTool(c)
	_, _ = tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b"}`)) //nolint:errcheck // headers already asserted
	_ = strings.EqualFold                                                                  // silence unused import if refactored
}
