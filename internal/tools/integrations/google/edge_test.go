package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClient_TokenFnError surfaces the token-source error branch.
func TestClient_TokenFnError(t *testing.T) {
	c, err := New(Config{
		TokenFn: func(context.Context) (string, error) {
			return "", assert.AnError
		},
		HTTPClient: http.DefaultClient,
	})
	require.NoError(t, err)
	err = c.do(context.Background(), http.MethodGet, "http://x", nil, nil)
	assert.ErrorContains(t, err, "token")
}

// TestClient_HTTPErrorSurfaces covers the non-2xx surface.
func TestClient_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":403,"message":"forbidden"}}`, http.StatusForbidden)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	err := c.do(context.Background(), http.MethodGet, srv.URL+"/x", nil, nil)
	assert.Contains(t, err.Error(), "403")
}

// TestClient_MalformedJSONResponse surfaces the decode-error branch.
func TestClient_MalformedJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	var out struct{ A string }
	err := c.do(context.Background(), http.MethodGet, srv.URL+"/x", nil, &out)
	assert.ErrorContains(t, err, "decode")
}

// TestGmailListTool_QueryPassthrough asserts the model's q parameter
// flows to Gmail without munging.
func TestGmailListTool_QueryPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "q=from")
		_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	tool := NewGmailListTool(c)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"q":"from:alice"}`))
	require.NoError(t, err)
}
