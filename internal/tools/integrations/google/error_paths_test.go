package google

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

// recordingServer captures the last request the tool actually sent so
// assertions can be made on method / path / query / headers / body.
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
		rec.Body, _ = io.ReadAll(r.Body) //nolint:errcheck // test fixture
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody)) //nolint:errcheck // test fixture
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// -- client.do error branches ----------------------------------------

func TestClientDo_MarshalBodyError(t *testing.T) {
	c, err := New(Config{AccessToken: "t"})
	require.NoError(t, err)
	// A channel is not JSON-encodable; do must fail before dialling.
	err = c.do(context.Background(), http.MethodPost, "http://127.0.0.1:1/x", make(chan int), nil)
	assert.ErrorContains(t, err, "marshal body")
}

func TestClientDo_BuildRequestError(t *testing.T) {
	c, err := New(Config{AccessToken: "t"})
	require.NoError(t, err)
	err = c.do(context.Background(), "BAD METHOD", "http://127.0.0.1:1/x", nil, nil)
	assert.ErrorContains(t, err, "build request")
}

func TestClientDo_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close() // nothing is listening any more
	c, err := New(Config{AccessToken: "t", HTTPClient: srv.Client()})
	require.NoError(t, err)
	err = c.do(context.Background(), http.MethodGet, dead+"/x", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "google: GET")
}

func TestClientDo_SendsBearerAndContentType(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"ok":true}`)
	c, err := New(Config{
		TokenFn:    func(context.Context) (string, error) { return "rotating-token", nil },
		HTTPClient: srv.Client(),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, c.do(context.Background(), http.MethodPost, srv.URL+"/p", map[string]any{"a": 1}, &out))

	assert.Equal(t, "Bearer rotating-token", rec.Header.Get("Authorization"))
	assert.Equal(t, "application/json", rec.Header.Get("Accept"))
	assert.Equal(t, "application/json", rec.Header.Get("Content-Type"))
	assert.JSONEq(t, `{"a":1}`, string(rec.Body))
	assert.Equal(t, true, out["ok"])
}

func TestClientDo_NoBodyOmitsContentType(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{}`)
	c := newTestClient(t, srv)
	require.NoError(t, c.do(context.Background(), http.MethodGet, srv.URL+"/g", nil, nil))
	assert.Empty(t, rec.Header.Get("Content-Type"))
}

func TestFirstNonEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"first wins", []string{"a", "b"}, "a"},
		{"skips empties", []string{"", "", "c"}, "c"},
		{"all empty", []string{"", ""}, ""},
		{"no args", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, firstNonEmpty(tc.in...))
		})
	}
}

func TestJSONString_EncodeError(t *testing.T) {
	_, err := jsonString(make(chan int))
	assert.ErrorContains(t, err, "encode")
}

// -- per-tool malformed input + upstream error branches ---------------

// toolFactory builds one tool bound to c.
type toolFactory struct {
	name    string
	build   func(*Client) tools.Tool
	valid   string // input that reaches the network
	badJSON string // input that must be rejected before the network
}

func allToolFactories() []toolFactory {
	return []toolFactory{
		{"gmail_list", func(c *Client) tools.Tool { return NewGmailListTool(c) }, `{"q":"x"}`, `{"q":`},
		{"gmail_get", func(c *Client) tools.Tool { return NewGmailGetTool(c) }, `{"id":"m1"}`, `not-json`},
		{"gmail_send", func(c *Client) tools.Tool { return NewGmailSendTool(c) }, `{"to":"a@b","subject":"s","body":"b"}`, `not-json`},
		{"calendar_list_events", func(c *Client) tools.Tool { return NewCalendarListEventsTool(c) }, `{}`, `{"max_results":`},
		{"calendar_create_event", func(c *Client) tools.Tool { return NewCalendarCreateEventTool(c) }, `{"summary":"s","start":"t0","end":"t1"}`, `not-json`},
		{"drive_search", func(c *Client) tools.Tool { return NewDriveSearchTool(c) }, `{"q":"x"}`, `not-json`},
		{"drive_get", func(c *Client) tools.Tool { return NewDriveGetTool(c) }, `{"id":"f1"}`, `not-json`},
	}
}

func TestEveryTool_RejectsMalformedJSONBeforeDialling(t *testing.T) {
	// Point the client at a listener that would fail loudly if used —
	// input validation must short-circuit first.
	c, err := New(Config{
		AccessToken:     "t",
		GmailBaseURL:    "http://127.0.0.1:1",
		CalendarBaseURL: "http://127.0.0.1:1",
		DriveBaseURL:    "http://127.0.0.1:1",
	})
	require.NoError(t, err)
	for _, f := range allToolFactories() {
		t.Run(f.name, func(t *testing.T) {
			_, err := f.build(c).Execute(context.Background(), json.RawMessage(f.badJSON))
			assert.ErrorContains(t, err, "bad input")
		})
	}
}

func TestEveryTool_PropagatesUpstreamRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Rate Limit Exceeded"}}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	for _, f := range allToolFactories() {
		t.Run(f.name, func(t *testing.T) {
			out, err := f.build(c).Execute(context.Background(), json.RawMessage(f.valid))
			require.Error(t, err)
			assert.Empty(t, out)
			assert.Contains(t, err.Error(), "429")
			assert.Contains(t, err.Error(), "Rate Limit Exceeded")
		})
	}
}

func TestEveryTool_HandlesEmptyJSONPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	for _, f := range allToolFactories() {
		t.Run(f.name, func(t *testing.T) {
			out, err := f.build(c).Execute(context.Background(), json.RawMessage(f.valid))
			require.NoError(t, err)
			assert.JSONEq(t, `{}`, out)
		})
	}
}

// -- calendar-specific request shaping --------------------------------

func TestCalendarListEvents_ExplicitWindowOverridesDefaultTimeMin(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"items":[]}`)
	tool := NewCalendarListEventsTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(
		`{"calendar_id":"team@example.com","time_min":"2026-01-01T00:00:00Z","time_max":"2026-02-01T00:00:00Z","max_results":5}`))
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, rec.Method)
	assert.Equal(t, "/calendar/calendars/team@example.com/events", rec.Path)
	assert.Equal(t, "2026-01-01T00:00:00Z", rec.Query.Get("timeMin"))
	assert.Equal(t, "2026-02-01T00:00:00Z", rec.Query.Get("timeMax"))
	assert.Equal(t, "5", rec.Query.Get("maxResults"))
	assert.Equal(t, "startTime", rec.Query.Get("orderBy"))
}

func TestCalendarListEvents_DefaultsTimeMinToNowAndOmitsTimeMax(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"items":[]}`)
	tool := NewCalendarListEventsTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)

	assert.Equal(t, "/calendar/calendars/primary/events", rec.Path)
	assert.NotEmpty(t, rec.Query.Get("timeMin"), "timeMin defaults to now so past events are excluded")
	assert.False(t, rec.Query.Has("timeMax"))
	assert.Equal(t, "30", rec.Query.Get("maxResults"))
}

func TestCalendarCreateEvent_IncludesOptionalDescription(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"id":"ev1"}`)
	tool := NewCalendarCreateEventTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(
		`{"calendar_id":"c1","summary":"s","description":"notes","start":"t0","end":"t1"}`))
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, rec.Method)
	assert.Equal(t, "/calendar/calendars/c1/events", rec.Path)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body, &body))
	assert.Equal(t, "notes", body["description"])
	assert.Equal(t, map[string]any{"dateTime": "t0"}, body["start"])
	assert.NotContains(t, body, "attendees", "attendees omitted when not supplied")
}

// -- gmail / drive request shaping ------------------------------------

func TestGmailGet_DefaultsToMetadataFormat(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"id":"m1"}`)
	tool := NewGmailGetTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"m/1"}`))
	require.NoError(t, err)
	assert.Equal(t, "/gmail/users/me/messages/m%2F1", rec.Escaped,
		"the message id must be path-escaped so a slash cannot forge a new path segment")
	assert.Equal(t, "metadata", rec.Query.Get("format"))
}

func TestDriveSearch_SetsFieldsAndPageSize(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"files":[]}`)
	tool := NewDriveSearchTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"q":"name contains 'x'","page_size":7}`))
	require.NoError(t, err)
	assert.Equal(t, "/drive/files", rec.Path)
	assert.Equal(t, "name contains 'x'", rec.Query.Get("q"))
	assert.Equal(t, "7", rec.Query.Get("pageSize"))
	assert.Contains(t, rec.Query.Get("fields"), "webViewLink")
}

func TestDriveGet_EscapesIDAndRequestsFields(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"id":"a b"}`)
	tool := NewDriveGetTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"a b"}`))
	require.NoError(t, err)
	assert.Equal(t, "/drive/files/a b", rec.Path)
	assert.Contains(t, rec.Query.Get("fields"), "size")
}

// -- register --------------------------------------------------------

func TestRegister_DuplicateToolNameFails(t *testing.T) {
	c, err := New(Config{AccessToken: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, reg.Register(NewGmailListTool(c)))
	err = Register(reg, c)
	assert.ErrorContains(t, err, "google: register gmail_list")
}
