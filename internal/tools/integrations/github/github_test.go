package github

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

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{Token: "test-pat", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	return c
}

func TestNew_MissingTokenErrors(t *testing.T) {
	t.Setenv(EnvToken, "")
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestNew_FallsBackToEnvToken(t *testing.T) {
	t.Setenv(EnvToken, "env-pat")
	c, err := New(Config{})
	require.NoError(t, err)
	assert.Equal(t, "env-pat", c.token)
}

func TestListReposTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/user/repos")
		assert.Equal(t, "Bearer test-pat", r.Header.Get("Authorization"))
		assert.Contains(t, r.URL.RawQuery, "visibility=private")
		_, _ = w.Write([]byte(`[{"full_name":"a/r","private":true,"stargazers_count":7}]`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	tool := NewListReposTool(c)
	assert.Equal(t, "github_list_repos", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.NotEmpty(t, tool.InputSchema())

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"visibility":"private"}`))
	require.NoError(t, err)
	assert.Contains(t, out, `"a/r"`)
}

func TestGetRepoTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/octocat/hello-world", r.URL.Path)
		_, _ = w.Write([]byte(`{"full_name":"octocat/hello-world"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewGetRepoTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"octocat","repo":"hello-world"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "octocat/hello-world")
}

func TestGetRepoTool_ValidatesInputs(t *testing.T) {
	tool := NewGetRepoTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":""}`))
	assert.ErrorContains(t, err, "required")
}

func TestSearchCodeTool_QueryEscaping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "q=func+handler")
		_, _ = w.Write([]byte(`{"items":[]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewSearchCodeTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"q":"func handler"}`))
	require.NoError(t, err)
}

func TestSearchCodeTool_QueryRequired(t *testing.T) {
	tool := NewSearchCodeTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.ErrorContains(t, err, "required")
}

func TestListPRsTool_StateDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "state=open")
		_, _ = w.Write([]byte(`[]`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewListPRsTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b"}`))
	require.NoError(t, err)
}

func TestGetPRTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/a/b/pulls/42", r.URL.Path)
		_, _ = w.Write([]byte(`{"number":42,"title":"foo"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewGetPRTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b","number":42}`))
	require.NoError(t, err)
	assert.Contains(t, out, "foo")
}

func TestCreateIssueTool_PostsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "hi", payload["title"])
		assert.Equal(t, "body", payload["body"])
		_, _ = w.Write([]byte(`{"number":1,"title":"hi"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewCreateIssueTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(),
		json.RawMessage(`{"owner":"a","repo":"b","title":"hi","body":"body","labels":["bug"]}`))
	require.NoError(t, err)
	assert.Contains(t, out, `"number":1`)
}

func TestCommentIssueTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/a/b/issues/42/comments", r.URL.Path)
		_, _ = w.Write([]byte(`{"id":123}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewCommentIssueTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"owner":"a","repo":"b","number":42,"body":"lgtm"}`))
	require.NoError(t, err)
}

func TestClient_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	tool := NewGetRepoTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"a","repo":"b"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "Bad credentials")
}

func TestRegister_AllTools(t *testing.T) {
	c, err := New(Config{Token: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	names := reg.Names()
	assert.Len(t, names, 9)
	for _, want := range []string{
		"github_list_repos", "github_get_repo", "github_search_code",
		"github_list_prs", "github_get_pr",
		"github_list_issues", "github_get_issue", "github_create_issue", "github_comment_issue",
	} {
		assert.Contains(t, strings.Join(names, ","), want)
	}
}

func TestRegister_DuplicateRejected(t *testing.T) {
	c, err := New(Config{Token: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	err = Register(reg, c)
	assert.Error(t, err)
}
