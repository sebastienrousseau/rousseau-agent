package stripe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

type recordedRequest struct {
	Method  string
	Path    string
	Escaped string
	Query   url.Values
	Header  http.Header
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
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody)) //nolint:errcheck // test fixture
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// -- client.get error branches ----------------------------------------

func TestGet_BuildRequestError(t *testing.T) {
	c, err := New(Config{SecretKey: "sk_x", BaseURL: "http://exa\x7fmple.invalid", HTTPClient: http.DefaultClient})
	require.NoError(t, err)
	err = c.get(context.Background(), "/charges", nil, nil)
	assert.ErrorContains(t, err, "build request")
}

func TestGet_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c := newTestClient(t, srv)
	srv.Close()
	err := c.get(context.Background(), "/charges", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stripe: /charges")
}

func TestGet_MalformedJSONResponse(t *testing.T) {
	srv, _ := newRecordingServer(t, http.StatusOK, `{"object":`)
	c := newTestClient(t, srv)
	var out any
	err := c.get(context.Background(), "/charges", nil, &out)
	assert.ErrorContains(t, err, "stripe: decode")
}

func TestGet_UsesBasicAuthWithKeyAsUsername(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
	c, err := New(Config{SecretKey: "sk_test_secret", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	require.NoError(t, c.get(context.Background(), "/charges", nil, nil))

	raw := strings.TrimPrefix(rec.Header.Get("Authorization"), "Basic ")
	decoded, dErr := base64.StdEncoding.DecodeString(raw)
	require.NoError(t, dErr)
	assert.Equal(t, "sk_test_secret:", string(decoded),
		"Stripe expects the secret key as the Basic-auth username with an empty password")
	assert.Equal(t, "application/json", rec.Header.Get("Accept"))
}

func TestGet_OmitsQuestionMarkForEmptyQuery(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
	c := newTestClient(t, srv)
	require.NoError(t, c.get(context.Background(), "/customers/cus_1", url.Values{}, nil))
	assert.Empty(t, rec.Query)
}

func TestJSONString_EncodeError(t *testing.T) {
	_, err := jsonString(make(chan int))
	assert.ErrorContains(t, err, "encode")
}

// -- every tool: malformed input + upstream failures ------------------

type stripeTool struct {
	name    string
	build   func(*Client) tools.Tool
	valid   string
	badJSON string
}

func allStripeTools() []stripeTool {
	return []stripeTool{
		{"stripe_list_charges", func(c *Client) tools.Tool { return NewListChargesTool(c) }, `{"limit":3}`, `{"limit":`},
		{"stripe_get_customer", func(c *Client) tools.Tool { return NewGetCustomerTool(c) }, `{"id":"cus_1"}`, `not-json`},
	}
}

func TestEveryTool_RejectsMalformedJSON(t *testing.T) {
	c, err := New(Config{SecretKey: "sk_x", BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	for _, tc := range allStripeTools() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.badJSON))
			assert.ErrorContains(t, err, "bad input")
		})
	}
}

func TestEveryTool_PropagatesRateLimit(t *testing.T) {
	srv, _ := newRecordingServer(t, http.StatusTooManyRequests,
		`{"error":{"type":"rate_limit_error","message":"Too many requests"}}`)
	c := newTestClient(t, srv)
	for _, tc := range allStripeTools() {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.valid))
			require.Error(t, err)
			assert.Empty(t, out)
			assert.Contains(t, err.Error(), "HTTP 429")
			assert.Contains(t, err.Error(), "Too many requests")
		})
	}
}

func TestEveryTool_HandlesEmptyPayload(t *testing.T) {
	srv, _ := newRecordingServer(t, http.StatusOK, `{}`)
	c := newTestClient(t, srv)
	for _, tc := range allStripeTools() {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.valid))
			require.NoError(t, err)
			assert.JSONEq(t, `{}`, out)
		})
	}
}

// -- request shaping ---------------------------------------------------

func TestListChargesTool_PaginatedListShape(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK,
		`{"object":"list","has_more":true,"data":[{"id":"ch_1","amount":500}]}`)
	tool := NewListChargesTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"customer":"cus_9","limit":1}`))
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, rec.Method)
	assert.Equal(t, "/charges", rec.Path)
	assert.Equal(t, "1", rec.Query.Get("limit"))
	assert.Equal(t, "cus_9", rec.Query.Get("customer"))

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, true, parsed["has_more"], "pagination metadata is passed through to the model")
}

func TestGetCustomerTool_EscapesID(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"id":"cus/1"}`)
	tool := NewGetCustomerTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"cus/1"}`))
	require.NoError(t, err)
	assert.Equal(t, "/customers/cus%2F1", rec.Escaped,
		"the customer id must be path-escaped so it cannot address another resource")
}

// -- register ----------------------------------------------------------

func TestRegister_DuplicateToolNameFails(t *testing.T) {
	c, err := New(Config{SecretKey: "sk_x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(NewListChargesTool(c)))
	err = Register(reg, c)
	assert.ErrorContains(t, err, "stripe: register stripe_list_charges")
}
