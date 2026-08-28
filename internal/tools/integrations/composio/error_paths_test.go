package composio

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

func TestDo_MarshalBodyError(t *testing.T) {
	c, err := New(Config{APIKey: "k", UserID: "u"})
	require.NoError(t, err)
	err = c.do(context.Background(), http.MethodPost, "/actions/execute", make(chan int), nil)
	assert.ErrorContains(t, err, "marshal body")
}

func TestDo_BuildRequestErrorOnInvalidBaseURL(t *testing.T) {
	c, err := New(Config{APIKey: "k", UserID: "u", BaseURL: "http://exa\x7fmple.invalid", HTTPClient: http.DefaultClient})
	require.NoError(t, err)
	err = c.do(context.Background(), http.MethodGet, "/actions", nil, nil)
	assert.ErrorContains(t, err, "build request")
}

func TestDo_DecodeResponseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, `{"items":`)
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv).List(context.Background())
	assert.ErrorContains(t, err, "decode response")
}

func TestListPage_PropagatesRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/actions", r.URL.Path)
		assert.Equal(t, "abc", r.URL.Query().Get("cursor"))
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		writeBody(t, w, `{"error":"rate limited"}`)
	}))
	defer srv.Close()
	out, err := newTestClient(t, srv).ListPage(context.Background(), "abc")
	require.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "HTTP 429")
	assert.Contains(t, err.Error(), "rate limited")
}

func TestRegister_DiscoveryFailureAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeBody(t, w, `{"error":"invalid api key"}`)
	}))
	defer srv.Close()
	reg := tools.NewRegistry()
	n, err := Register(context.Background(), reg, newTestClient(t, srv), nil)
	require.Error(t, err)
	assert.Zero(t, n)
	assert.Contains(t, err.Error(), "discover actions")
	assert.Empty(t, reg.Names())
}

func TestListPage_EmptyPayloadYieldsNoActions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, `{}`)
	}))
	defer srv.Close()
	actions, err := newTestClient(t, srv).List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, actions)
}

func TestExecute_OmitsInputWhenParamsEmpty(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body) //nolint:errcheck // fixed-size body
		writeBody(t, w, `{"successful":true}`)
	}))
	defer srv.Close()

	raw, err := newTestClient(t, srv).Execute(context.Background(), "GMAIL_SEND", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"successful":true}`, string(raw))

	var sent map[string]any
	require.NoError(t, json.Unmarshal(body, &sent))
	assert.Equal(t, map[string]any{"userId": "user-1", "actionName": "GMAIL_SEND"}, sent)
}
