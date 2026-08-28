package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{
		AccessToken:     "test-at",
		HTTPClient:      srv.Client(),
		GmailBaseURL:    srv.URL + "/gmail",
		CalendarBaseURL: srv.URL + "/calendar",
		DriveBaseURL:    srv.URL + "/drive",
	})
	require.NoError(t, err)
	return c
}

func TestNew_RequiresToken(t *testing.T) {
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestNew_TokenFnPreferred(t *testing.T) {
	called := 0
	c, err := New(Config{TokenFn: func(context.Context) (string, error) {
		called++
		return "fresh", nil
	}})
	require.NoError(t, err)
	tok, err := c.tokenFn(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "fresh", tok)
	assert.Equal(t, 1, called)
}

func TestGmailList_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/users/me/messages")
		assert.Equal(t, "Bearer test-at", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"messages":[{"id":"m1"}]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewGmailListTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"q":"is:unread"}`))
	require.NoError(t, err)
	assert.Contains(t, out, `"id":"m1"`)
}

func TestGmailGet_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/messages/m1")
		_, _ = w.Write([]byte(`{"id":"m1","snippet":"hello"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewGmailGetTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"m1"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "hello")
}

func TestGmailSend_EncodesRFC5322(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/gmail/users/me/messages/send", r.URL.Path)
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var payload struct {
			Raw string `json:"raw"`
		}
		require.NoError(t, json.Unmarshal(body, &payload))
		raw, err := base64.URLEncoding.DecodeString(payload.Raw)
		require.NoError(t, err)
		assert.Contains(t, string(raw), "To: alice@example.com")
		assert.Contains(t, string(raw), "Subject: hi")
		assert.Contains(t, string(raw), "hello body")
		_, _ = w.Write([]byte(`{"id":"sent"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewGmailSendTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"to":"alice@example.com","subject":"hi","body":"hello body"}`))
	require.NoError(t, err)
}

func TestGmailSend_ValidatesInput(t *testing.T) {
	tool := NewGmailSendTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"to":"a@b"}`))
	assert.ErrorContains(t, err, "required")
}

func TestCalendarListEvents_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/calendars/primary/events")
		assert.Contains(t, r.URL.RawQuery, "singleEvents=true")
		_, _ = w.Write([]byte(`{"items":[]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewCalendarListEventsTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
}

func TestCalendarCreateEvent_ShapesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "meeting", payload["summary"])
		attendees := payload["attendees"].([]any)
		require.Len(t, attendees, 1)
		_, _ = w.Write([]byte(`{"id":"ev1"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewCalendarCreateEventTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"summary":"meeting","start":"2026-07-17T10:00:00Z","end":"2026-07-17T11:00:00Z","attendees":["a@b"]}`))
	require.NoError(t, err)
}

func TestDriveSearch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/files")
		assert.Contains(t, r.URL.RawQuery, "q=name")
		_, _ = w.Write([]byte(`{"files":[]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewDriveSearchTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"q":"name contains 'foo'"}`))
	require.NoError(t, err)
}

func TestDriveGet_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/files/abc")
		_, _ = w.Write([]byte(`{"id":"abc","name":"file"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewDriveGetTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"abc"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "file")
}

func TestClient_ErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"scope missing"}}`, http.StatusForbidden)
	}))
	defer srv.Close()
	tool := NewGmailListTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestRegister_AllTools(t *testing.T) {
	c, err := New(Config{AccessToken: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	names := reg.Names()
	assert.Len(t, names, 7)
	for _, want := range []string{
		"gmail_list", "gmail_get", "gmail_send",
		"calendar_list_events", "calendar_create_event",
		"drive_search", "drive_get",
	} {
		assert.Contains(t, strings.Join(names, ","), want)
	}
}
