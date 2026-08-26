package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// recordedRequest captures what the tool actually put on the wire.
type recordedRequest struct {
	Method  string
	Path    string
	Escaped string
	Query   url.Values
	Header  http.Header
	Body    []byte
}

func newRecordingServer(t *testing.T, status int, respBody string) (*httptest.Server, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Method = r.Method
		rec.Path = r.URL.Path
		rec.Escaped = r.URL.EscapedPath()
		rec.Query = r.URL.Query()
		rec.Header = r.Header.Clone()
		rec.Body, _ = io.ReadAll(r.Body) //nolint:errcheck // test setup
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody)) //nolint:errcheck // test handler
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// -- client.do error branches ----------------------------------------

func TestClientDo_MarshalBodyError(t *testing.T) {
	c, err := New(Config{Token: "t"})
	require.NoError(t, err)
	err = c.do(context.Background(), http.MethodPost, "/x", make(chan int), nil)
	assert.ErrorContains(t, err, "marshal body")
}

func TestClientDo_BuildRequestError(t *testing.T) {
	c, err := New(Config{Token: "t"})
	require.NoError(t, err)
	err = c.do(context.Background(), "BAD METHOD", "/x", nil, nil)
	assert.ErrorContains(t, err, "build request")
}

func TestClientDo_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	srv.Close() // nothing is listening any more
	err = c.do(context.Background(), http.MethodGet, "/x", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github: GET /x")
}

func TestClientDo_SendsAPIVersionAndAuthHeaders(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"ok":true}`)
	c, err := New(Config{Token: "ghp_secret", BaseURL: srv.URL, HTTPClient: srv.Client(), UserAgent: "custom-ua/9"})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, c.do(context.Background(), http.MethodPost, "/x", map[string]any{"k": "v"}, &out))

	assert.Equal(t, "Bearer ghp_secret", rec.Header.Get("Authorization"))
	assert.Equal(t, "application/vnd.github+json", rec.Header.Get("Accept"))
	assert.Equal(t, "2022-11-28", rec.Header.Get("X-GitHub-Api-Version"))
	assert.Equal(t, "custom-ua/9", rec.Header.Get("User-Agent"))
	assert.Equal(t, "application/json", rec.Header.Get("Content-Type"))
	assert.JSONEq(t, `{"k":"v"}`, string(rec.Body))
	assert.Equal(t, true, out["ok"])
}

func TestJSONString_EncodeError(t *testing.T) {
	_, err := jsonString(make(chan int))
	assert.ErrorContains(t, err, "encode")
}

// -- every tool: malformed input + upstream failures ------------------

type ghTool struct {
	name    string
	build   func(*Client) tools.Tool
	valid   string
	badJSON string
}

func allGitHubTools() []ghTool {
	return []ghTool{
		{"github_list_repos", func(c *Client) tools.Tool { return NewListReposTool(c) }, `{"visibility":"all"}`, `{"visibility":`},
		{"github_get_repo", func(c *Client) tools.Tool { return NewGetRepoTool(c) }, `{"owner":"o","repo":"r"}`, `not-json`},
		{"github_search_code", func(c *Client) tools.Tool { return NewSearchCodeTool(c) }, `{"q":"x"}`, `not-json`},
		{"github_list_prs", func(c *Client) tools.Tool { return NewListPRsTool(c) }, `{"owner":"o","repo":"r"}`, `not-json`},
		{"github_get_pr", func(c *Client) tools.Tool { return NewGetPRTool(c) }, `{"owner":"o","repo":"r","number":1}`, `not-json`},
		{"github_list_issues", func(c *Client) tools.Tool { return NewListIssuesTool(c) }, `{"owner":"o","repo":"r"}`, `not-json`},
		{"github_get_issue", func(c *Client) tools.Tool { return NewGetIssueTool(c) }, `{"owner":"o","repo":"r","number":1}`, `not-json`},
		{"github_create_issue", func(c *Client) tools.Tool { return NewCreateIssueTool(c) }, `{"owner":"o","repo":"r","title":"t"}`, `not-json`},
		{"github_comment_issue", func(c *Client) tools.Tool { return NewCommentIssueTool(c) }, `{"owner":"o","repo":"r","number":1,"body":"b"}`, `not-json`},
	}
}

func TestEveryTool_RejectsMalformedJSON(t *testing.T) {
	c, err := New(Config{Token: "t", BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	for _, tc := range allGitHubTools() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.badJSON))
			assert.ErrorContains(t, err, "bad input")
		})
	}
}

func TestEveryTool_PropagatesSecondaryRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit"}`)) //nolint:errcheck // test handler
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	for _, tc := range allGitHubTools() {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.valid))
			require.Error(t, err)
			assert.Empty(t, out)
			assert.Contains(t, err.Error(), "HTTP 403")
			assert.Contains(t, err.Error(), "secondary rate limit")
		})
	}
}

func TestEveryTool_HandlesEmptyUpstreamPayload(t *testing.T) {
	// List endpoints decode into a slice, singular ones into `any`; an
	// empty JSON array satisfies both without a decode error.
	for _, tc := range allGitHubTools() {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`[]`)) //nolint:errcheck // test handler
			}))
			defer srv.Close()
			out, err := tc.build(newTestClient(t, srv)).Execute(context.Background(), json.RawMessage(tc.valid))
			require.NoError(t, err)
			assert.Equal(t, "[]", out)
		})
	}
}

func TestEveryTool_SurfacesMalformedUpstreamJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"truncated":`)) //nolint:errcheck // test handler
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	for _, tc := range allGitHubTools() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.valid))
			assert.ErrorContains(t, err, "decode response")
		})
	}
}

// -- required-argument validation -------------------------------------

func TestEveryRepoScopedTool_RequiresOwnerAndRepo(t *testing.T) {
	c, err := New(Config{Token: "t", BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		build func(*Client) tools.Tool
		input string
	}{
		{"list_issues/no owner", func(c *Client) tools.Tool { return NewListIssuesTool(c) }, `{"repo":"r"}`},
		{"list_issues/no repo", func(c *Client) tools.Tool { return NewListIssuesTool(c) }, `{"owner":"o"}`},
		{"list_prs/no repo", func(c *Client) tools.Tool { return NewListPRsTool(c) }, `{"owner":"o"}`},
		{"get_repo/no repo", func(c *Client) tools.Tool { return NewGetRepoTool(c) }, `{"owner":"o"}`},
		{"get_pr/no number", func(c *Client) tools.Tool { return NewGetPRTool(c) }, `{"owner":"o","repo":"r"}`},
		{"get_issue/no number", func(c *Client) tools.Tool { return NewGetIssueTool(c) }, `{"owner":"o","repo":"r"}`},
		{"create_issue/no title", func(c *Client) tools.Tool { return NewCreateIssueTool(c) }, `{"owner":"o","repo":"r"}`},
		{"comment_issue/no body", func(c *Client) tools.Tool { return NewCommentIssueTool(c) }, `{"owner":"o","repo":"r","number":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.input))
			assert.ErrorContains(t, err, "required")
		})
	}
}

// -- request shaping ---------------------------------------------------

func TestRepoScopedTools_EscapeOwnerAndRepoPathSegments(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
	tool := NewGetRepoTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"a/b","repo":"c d"}`))
	require.NoError(t, err)
	assert.Equal(t, "/repos/a%2Fb/c%20d", rec.Escaped,
		"owner/repo must be path-escaped so they cannot inject extra path segments")
}

func TestCreateIssueTool_OmitsEmptyOptionalFields(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"number":1}`)
	tool := NewCreateIssueTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"o","repo":"r","title":"only-title"}`))
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, rec.Method)
	assert.Equal(t, "/repos/o/r/issues", rec.Path)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body, &body))
	assert.Equal(t, map[string]any{"title": "only-title"}, body)
}

func TestCreateIssueTool_ForwardsLabels(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"number":1}`)
	tool := NewCreateIssueTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"owner":"o","repo":"r","title":"t","body":"b","labels":["bug","p0"]}`))
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body, &body))
	assert.Equal(t, "b", body["body"])
	assert.Equal(t, []any{"bug", "p0"}, body["labels"])
}

func TestCommentIssueTool_PostsOnlyBody(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"id":1}`)
	tool := NewCommentIssueTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"owner":"o","repo":"r","number":7,"body":"LGTM"}`))
	require.NoError(t, err)
	assert.Equal(t, "/repos/o/r/issues/7/comments", rec.Path)
	assert.JSONEq(t, `{"body":"LGTM"}`, string(rec.Body))
}

func TestListIssuesTool_DefaultsStateAndPerPage(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `[]`)
	tool := NewListIssuesTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"owner":"o","repo":"r"}`))
	require.NoError(t, err)
	assert.Equal(t, "open", rec.Query.Get("state"))
	assert.Equal(t, "30", rec.Query.Get("per_page"))
	assert.False(t, rec.Query.Has("labels"))
}

func TestListReposTool_ParsesPaginatedPage(t *testing.T) {
	// A page of results plus a Link header — the client keeps only the
	// selected fields and ignores pagination metadata.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `<https://api.github.com/user/repos?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"full_name":"a/one","private":false,"stargazers_count":3,"language":"Go","ignored":"x"},` + //nolint:errcheck // test setup
			`{"full_name":"a/two","private":true,"stargazers_count":0}]`))
	}))
	defer srv.Close()
	tool := NewListReposTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"per_page":2}`))
	require.NoError(t, err)

	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "a/one", got[0]["full_name"])
	assert.EqualValues(t, 3, got[0]["stargazers_count"])
	assert.NotContains(t, got[0], "ignored", "unknown upstream fields are dropped by the typed decode")
}
