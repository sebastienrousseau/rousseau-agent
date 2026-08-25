package slack

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// Form decodes an application/x-www-form-urlencoded request body.
func (r *recordedRequest) Form(t *testing.T) url.Values {
	t.Helper()
	v, err := url.ParseQuery(string(r.Body))
	require.NoError(t, err)
	return v
}

func newRecordingServer(t *testing.T, status int, respBody string) (*httptest.Server, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Method = r.Method
		rec.Path = r.URL.Path
		rec.Query = r.URL.Query()
		rec.Header = r.Header.Clone()
		rec.Body, _ = io.ReadAll(r.Body) //nolint:errcheck // test fixture
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody)) //nolint:errcheck // test fixture
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// badURLClient points at a URL containing a control character so
// http.NewRequestWithContext fails during URL parsing.
func badURLClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{BotToken: "xoxb-x", BaseURL: "http://exa\x7fmple.invalid", HTTPClient: http.DefaultClient})
	require.NoError(t, err)
	return c
}

// -- transport-layer error branches -----------------------------------

func TestPostForm_BuildRequestError(t *testing.T) {
	err := badURLClient(t).postForm(context.Background(), "reactions.add", url.Values{}, nil)
	assert.ErrorContains(t, err, "build request")
}

func TestPostJSON_BuildRequestError(t *testing.T) {
	err := badURLClient(t).postJSON(context.Background(), "chat.postMessage", map[string]any{"a": 1}, nil)
	assert.ErrorContains(t, err, "build request")
}

func TestGet_BuildRequestError(t *testing.T) {
	err := badURLClient(t).get(context.Background(), "conversations.list", url.Values{}, nil)
	assert.ErrorContains(t, err, "build request")
}

func TestPostJSON_MarshalBodyError(t *testing.T) {
	c, err := New(Config{BotToken: "xoxb-x"})
	require.NoError(t, err)
	err = c.postJSON(context.Background(), "chat.postMessage", make(chan int), nil)
	assert.ErrorContains(t, err, "marshal body")
}

func TestDo_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c := newTestClient(t, srv)
	srv.Close()
	err := c.get(context.Background(), "conversations.list", url.Values{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/conversations.list")
}

// TestDo_TruncatedBodyError drives the io.ReadAll failure branch.
func TestDo_TruncatedBodyError(t *testing.T) {
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
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 500\r\n\r\n{\"ok\":true}"))
		_ = conn.Close()
	}()
	c, err := New(Config{BotToken: "xoxb-x", BaseURL: "http://" + ln.Addr().String()})
	require.NoError(t, err)
	err = c.get(context.Background(), "conversations.list", url.Values{}, nil)
	assert.ErrorContains(t, err, "read body")
}

func TestDo_MalformedEnvelope(t *testing.T) {
	srv, _ := newRecordingServer(t, http.StatusOK, `<html>gateway</html>`)
	c := newTestClient(t, srv)
	err := c.get(context.Background(), "conversations.list", url.Values{}, nil)
	assert.ErrorContains(t, err, "decode envelope")
}

func TestDo_PayloadTypeMismatch(t *testing.T) {
	// ok:true, but `channel` arrives as an object where the tool models
	// it as a string — the payload decode must surface that.
	srv, _ := newRecordingServer(t, http.StatusOK, `{"ok":true,"channel":{"id":"C1"},"ts":"1.2"}`)
	tool := NewPostMessageTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1","text":"hi"}`))
	assert.ErrorContains(t, err, "decode payload")
}

func TestDo_HTTPErrorIncludesStatusAndBody(t *testing.T) {
	srv, _ := newRecordingServer(t, http.StatusTooManyRequests, `{"ok":false,"error":"ratelimited"}`)
	c := newTestClient(t, srv)
	err := c.get(context.Background(), "conversations.list", url.Values{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 429")
	assert.Contains(t, err.Error(), "ratelimited")
}

// -- every tool: malformed input + upstream failure -------------------

type slackTool struct {
	name    string
	build   func(*Client) tools.Tool
	valid   string
	badJSON string
}

func allSlackTools() []slackTool {
	return []slackTool{
		{"slack_post_message", func(c *Client) tools.Tool { return NewPostMessageTool(c) }, `{"channel":"C1","text":"hi"}`, `not-json`},
		{"slack_get_thread", func(c *Client) tools.Tool { return NewGetThreadTool(c) }, `{"channel":"C1","thread_ts":"1.2"}`, `not-json`},
		{"slack_add_reaction", func(c *Client) tools.Tool { return NewAddReactionTool(c) }, `{"channel":"C1","timestamp":"1.2","name":"tada"}`, `not-json`},
		{"slack_list_channels", func(c *Client) tools.Tool { return NewListChannelsTool(c) }, `{}`, `{"limit":`},
	}
}

func TestEveryTool_RejectsMalformedJSON(t *testing.T) {
	c, err := New(Config{BotToken: "xoxb-x", BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	for _, tc := range allSlackTools() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.badJSON))
			assert.ErrorContains(t, err, "bad input")
		})
	}
}

func TestEveryTool_PropagatesSlackAPIError(t *testing.T) {
	// Slack signals failures with HTTP 200 + {"ok":false,"error":...}.
	srv, _ := newRecordingServer(t, http.StatusOK, `{"ok":false,"error":"channel_not_found"}`)
	c := newTestClient(t, srv)
	for _, tc := range allSlackTools() {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.valid))
			require.Error(t, err)
			assert.Empty(t, out)
			assert.Contains(t, err.Error(), "channel_not_found")
		})
	}
}

func TestEveryTool_PropagatesRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error":"ratelimited"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	for _, tc := range allSlackTools() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.valid))
			assert.ErrorContains(t, err, "HTTP 429")
		})
	}
}

func TestEveryTool_RequiredArguments(t *testing.T) {
	c, err := New(Config{BotToken: "xoxb-x", BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		build func(*Client) tools.Tool
		input string
	}{
		{"post_message/no text", func(c *Client) tools.Tool { return NewPostMessageTool(c) }, `{"channel":"C1"}`},
		{"get_thread/no thread_ts", func(c *Client) tools.Tool { return NewGetThreadTool(c) }, `{"channel":"C1"}`},
		{"add_reaction/no name", func(c *Client) tools.Tool { return NewAddReactionTool(c) }, `{"channel":"C1","timestamp":"1.2"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build(c).Execute(context.Background(), json.RawMessage(tc.input))
			assert.ErrorContains(t, err, "required")
		})
	}
}

// -- request shaping ---------------------------------------------------

func TestGetThreadTool_SendsChannelAndTS(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"ok":true,"messages":[{"text":"reply"}]}`)
	tool := NewGetThreadTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1","thread_ts":"1700000000.000100"}`))
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, rec.Method)
	assert.Equal(t, "/conversations.replies", rec.Path)
	assert.Equal(t, "C1", rec.Query.Get("channel"))
	assert.Equal(t, "1700000000.000100", rec.Query.Get("ts"))
	assert.Equal(t, "Bearer xoxb-test", rec.Header.Get("Authorization"))
	assert.Contains(t, out, "reply")
}

func TestAddReactionTool_PostsURLEncodedForm(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"ok":true}`)
	tool := NewAddReactionTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(),
		json.RawMessage(`{"channel":"C1","timestamp":"1.2","name":"tada"}`))
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, rec.Method)
	assert.Equal(t, "/reactions.add", rec.Path)
	assert.Equal(t, "application/x-www-form-urlencoded", rec.Header.Get("Content-Type"))
	form := rec.Form(t)
	assert.Equal(t, "C1", form.Get("channel"))
	assert.Equal(t, "1.2", form.Get("timestamp"))
	assert.Equal(t, "tada", form.Get("name"))
	assert.JSONEq(t, `{"ok":true}`, out)
}

func TestListChannelsTool_AppliesDefaults(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"ok":true,"channels":[]}`)
	tool := NewListChannelsTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "/conversations.list", rec.Path)
	assert.Equal(t, "public_channel", rec.Query.Get("types"))
	assert.Equal(t, "100", rec.Query.Get("limit"))
}

func TestPostMessageTool_OmitsThreadTSWhenAbsent(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"ok":true,"channel":"C1","ts":"1.2"}`)
	tool := NewPostMessageTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1","text":"hi"}`))
	require.NoError(t, err)

	assert.Equal(t, "application/json; charset=utf-8", rec.Header.Get("Content-Type"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body, &body))
	assert.Equal(t, map[string]any{"channel": "C1", "text": "hi"}, body)
	assert.JSONEq(t, `{"ok":true,"channel":"C1","ts":"1.2"}`, out)
}

func TestJSONString_EncodeError(t *testing.T) {
	_, err := jsonString(make(chan int))
	assert.ErrorContains(t, err, "encode")
}

// -- register ----------------------------------------------------------

func TestRegister_DuplicateToolNameFails(t *testing.T) {
	c, err := New(Config{BotToken: "xoxb-x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(NewPostMessageTool(c)))
	err = Register(reg, c)
	assert.ErrorContains(t, err, "slack: register slack_post_message")
}
