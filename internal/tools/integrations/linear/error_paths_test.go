package linear

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// gqlRequest is the decoded GraphQL envelope the tool sent upstream.
type gqlRequest struct {
	Method    string
	Header    http.Header
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// newGQLServer records the single GraphQL POST and replies with resp.
func newGQLServer(t *testing.T, status int, resp string) (*httptest.Server, *gqlRequest) {
	t.Helper()
	rec := &gqlRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Method = r.Method
		rec.Header = r.Header.Clone()
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		_ = json.Unmarshal(body, rec) //nolint:errcheck // assertions cover the parsed shape
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp)) //nolint:errcheck // test fixture
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// -- client.query error branches --------------------------------------

func TestQuery_MarshalVariablesError(t *testing.T) {
	c, err := New(Config{APIKey: "k"})
	require.NoError(t, err)
	// A channel in the variables map cannot be encoded; the client must
	// fail before opening a socket.
	err = c.query(context.Background(), "query {}", map[string]any{"bad": make(chan int)}, nil)
	assert.ErrorContains(t, err, "marshal body")
}

func TestQuery_BuildRequestError(t *testing.T) {
	c, err := New(Config{APIKey: "k", BaseURL: "http://exa\x7fmple.invalid"})
	require.NoError(t, err)
	err = c.query(context.Background(), "query {}", nil, nil)
	assert.ErrorContains(t, err, "build request")
}

func TestQuery_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c, err := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	srv.Close()
	err = c.query(context.Background(), "query {}", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linear: post")
}

// TestQuery_TruncatedBodyError drives the io.ReadAll failure branch: the
// server promises more bytes than it delivers, then drops the socket.
func TestQuery_TruncatedBodyError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	go func() {
		conn, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf) //nolint:errcheck // best-effort fixture
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 500\r\n\r\n{\"data\":{}}"))
		_ = conn.Close()
	}()

	c, err := New(Config{APIKey: "k", BaseURL: "http://" + ln.Addr().String()})
	require.NoError(t, err)
	err = c.query(context.Background(), "query {}", nil, nil)
	assert.ErrorContains(t, err, "read body")
}

func TestQuery_DecodeDataTypeMismatch(t *testing.T) {
	srv, _ := newGQLServer(t, http.StatusOK, `{"data":"a bare string, not an object"}`)
	c, err := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	var out struct {
		Issues struct{ Nodes []any } `json:"issues"`
	}
	err = c.query(context.Background(), "query {}", nil, &out)
	assert.ErrorContains(t, err, "decode data")
}

func TestQuery_SendsAuthorizationAndJSONBody(t *testing.T) {
	srv, rec := newGQLServer(t, http.StatusOK, `{"data":{"ok":true}}`)
	c, err := New(Config{APIKey: "lin_api_secret", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, c.query(context.Background(), "query Q {}", map[string]any{"v": 1}, &out))

	assert.Equal(t, http.MethodPost, rec.Method)
	// Linear uses the raw API key, not a Bearer prefix.
	assert.Equal(t, "lin_api_secret", rec.Header.Get("Authorization"))
	assert.Equal(t, "application/json", rec.Header.Get("Content-Type"))
	assert.Equal(t, "query Q {}", rec.Query)
	assert.EqualValues(t, 1, rec.Variables["v"])
	assert.Equal(t, true, out["ok"])
}

func TestQuery_OmitsVariablesWhenNil(t *testing.T) {
	srv, rec := newGQLServer(t, http.StatusOK, `{"data":{}}`)
	c, err := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	require.NoError(t, c.query(context.Background(), "query {}", nil, nil))
	assert.Nil(t, rec.Variables)
}

func TestJSONString_EncodeError(t *testing.T) {
	_, err := jsonString(make(chan int))
	assert.ErrorContains(t, err, "encode")
}

// -- every tool: malformed input + upstream failure -------------------

type linearTool struct {
	name    string
	build   func(*Client) tools.Tool
	valid   string
	badJSON string
}

func allLinearTools() []linearTool {
	return []linearTool{
		{"linear_list_issues", func(c *Client) tools.Tool { return NewListIssuesTool(c) }, `{"team_key":"ENG"}`, `{"first":`},
		{"linear_get_issue", func(c *Client) tools.Tool { return NewGetIssueTool(c) }, `{"identifier":"ENG-1"}`, `not-json`},
		{"linear_create_issue", func(c *Client) tools.Tool { return NewCreateIssueTool(c) }, `{"team_id":"t","title":"x"}`, `not-json`},
		{"linear_update_issue", func(c *Client) tools.Tool { return NewUpdateIssueTool(c) }, `{"id":"i","title":"x"}`, `not-json`},
	}
}

func TestEveryTool_RejectsMalformedJSON(t *testing.T) {
	c, err := New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	for _, tc := range allLinearTools() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.badJSON))
			assert.ErrorContains(t, err, "bad input")
		})
	}
}

func TestEveryTool_PropagatesRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Requests-Remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Ratelimit exceeded"}]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	for _, tc := range allLinearTools() {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.valid))
			require.Error(t, err)
			assert.Empty(t, out)
			// HTTP status wins over the GraphQL error envelope.
			assert.Contains(t, err.Error(), "HTTP 429")
			assert.Contains(t, err.Error(), "Ratelimit exceeded")
		})
	}
}

func TestEveryTool_PropagatesGraphQLErrorOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Linear answers 200 with an `errors` array for auth/validation
		// failures — the client must not treat that as success.
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"Entity not found"}]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	for _, tc := range allLinearTools() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.valid))
			assert.ErrorContains(t, err, "Entity not found")
		})
	}
}

// -- request shaping ---------------------------------------------------

func TestListIssuesTool_BuildsBothFilters(t *testing.T) {
	srv, rec := newGQLServer(t, http.StatusOK, `{"data":{"issues":{"nodes":[]}}}`)
	tool := NewListIssuesTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"team_key":"ENG","state":"In Progress","first":5}`))
	require.NoError(t, err)

	assert.EqualValues(t, 5, rec.Variables["first"])
	filter, ok := rec.Variables["filter"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t,
		map[string]any{"key": map[string]any{"eq": "ENG"}},
		filter["team"])
	assert.Equal(t,
		map[string]any{"name": map[string]any{"eq": "In Progress"}},
		filter["state"],
		"a state filter must be translated into Linear's nested eq shape")
}

func TestListIssuesTool_NilInputSendsEmptyFilterAndDefaultPageSize(t *testing.T) {
	srv, rec := newGQLServer(t, http.StatusOK, `{"data":{"issues":{"nodes":[]}}}`)
	tool := NewListIssuesTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"issues":{"nodes":[]}}`, out)
	assert.EqualValues(t, 25, rec.Variables["first"])
	assert.Equal(t, map[string]any{}, rec.Variables["filter"])
}

func TestCreateIssueTool_OmitsUnsetOptionalFields(t *testing.T) {
	srv, rec := newGQLServer(t, http.StatusOK, `{"data":{"issueCreate":{"success":true}}}`)
	tool := NewCreateIssueTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"team_id":"t1","title":"only"}`))
	require.NoError(t, err)

	input, ok := rec.Variables["input"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"teamId": "t1", "title": "only"}, input,
		"zero-valued description/priority must not be sent")
}

func TestUpdateIssueTool_SendsOnlyProvidedFields(t *testing.T) {
	srv, rec := newGQLServer(t, http.StatusOK, `{"data":{"issueUpdate":{"success":true}}}`)
	tool := NewUpdateIssueTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"i1","state_id":"s1"}`))
	require.NoError(t, err)

	assert.Equal(t, "i1", rec.Variables["id"])
	assert.Equal(t, map[string]any{"stateId": "s1"}, rec.Variables["input"])
}

// -- register ----------------------------------------------------------

func TestRegister_DuplicateToolNameFails(t *testing.T) {
	c, err := New(Config{APIKey: "k"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(NewListIssuesTool(c)))
	err = Register(reg, c)
	assert.ErrorContains(t, err, "linear: register linear_list_issues")
}
