package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{SecretKey: "sk_test_x", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	return c
}

func TestNew_RequiresKey(t *testing.T) {
	t.Setenv(EnvToken, "")
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestNew_EnvFallback(t *testing.T) {
	t.Setenv(EnvToken, "sk_env")
	c, err := New(Config{})
	require.NoError(t, err)
	assert.Equal(t, "sk_env", c.key)
}

func TestListChargesTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/charges", r.URL.Path)
		user, _, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "sk_test_x", user)
		assert.Equal(t, "5", r.URL.Query().Get("limit"))
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewListChargesTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"limit":5}`))
	require.NoError(t, err)
	assert.Contains(t, out, `"object":"list"`)
}

func TestListChargesTool_DefaultLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "10", r.URL.Query().Get("limit"))
		_, _ = w.Write([]byte(`{"data":[]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewListChargesTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
}

func TestGetCustomerTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/customers/cus_123", r.URL.Path)
		_, _ = w.Write([]byte(`{"id":"cus_123","email":"a@b"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewGetCustomerTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"cus_123"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "a@b")
}

func TestGetCustomerTool_ValidatesInput(t *testing.T) {
	c, err := New(Config{SecretKey: "x"})
	require.NoError(t, err)
	tool := NewGetCustomerTool(c)
	_, err = tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.ErrorContains(t, err, "required")
}

func TestClient_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"No such customer"}}`, http.StatusNotFound)
	}))
	defer srv.Close()
	tool := NewGetCustomerTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"cus_x"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestRegister_AllTools(t *testing.T) {
	c, err := New(Config{SecretKey: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	assert.Len(t, reg.Names(), 2)
}
