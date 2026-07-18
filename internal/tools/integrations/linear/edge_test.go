package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClient_HTTPErrorSurfaces covers the non-2xx branch.
func TestClient_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, err := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	err = c.query(context.Background(), "query { viewer { id } }", nil, nil)
	assert.ErrorContains(t, err, "500")
}

// TestClient_MalformedResponse covers the JSON-decode branch.
func TestClient_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	err = c.query(context.Background(), "query { }", nil, nil)
	assert.ErrorContains(t, err, "decode")
}

func TestCreateIssueTool_Passthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true}}}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewCreateIssueTool(c)
	_, err = tool.Execute(context.Background(),
		json.RawMessage(`{"team_id":"t","title":"hi","description":"body","priority":3}`))
	require.NoError(t, err)
}

func TestUpdateIssueTool_MultipleFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true}}}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewUpdateIssueTool(c)
	_, err = tool.Execute(context.Background(),
		json.RawMessage(`{"id":"i","title":"new","description":"body","state_id":"s","priority":1}`))
	require.NoError(t, err)
}
