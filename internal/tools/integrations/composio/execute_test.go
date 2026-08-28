package composio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAction_ExecuteRoundTrip exercises the dynamic action wrapper's
// Execute path — proxies the model's input into Composio and returns
// the raw JSON body as the tool result.
func TestAction_ExecuteRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/actions/execute", r.URL.Path)
		writeBody(t, w, `{"result":{"messageId":"m1"}}`)
	}))
	defer srv.Close()
	c, err := New(Config{APIKey: "k", UserID: "u", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	act := &action{
		c:      c,
		spec:   Action{Name: "GMAIL_SEND", AppKey: "gmail", Parameters: json.RawMessage(`{"type":"object"}`)},
		toolID: "cx_gmail_gmail_send",
	}
	out, err := act.Execute(context.Background(), json.RawMessage(`{"to":"a@b"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "messageId")
}

func TestAction_ExecuteWireErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, err := New(Config{APIKey: "k", UserID: "u", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	act := &action{
		c:      c,
		spec:   Action{Name: "GMAIL_SEND", AppKey: "gmail"},
		toolID: "cx_gmail_gmail_send",
	}
	_, err = act.Execute(context.Background(), nil)
	assert.ErrorContains(t, err, "500")
}

func TestAction_Name(t *testing.T) {
	a := &action{toolID: "cx_x_foo"}
	assert.Equal(t, "cx_x_foo", a.Name())
}
