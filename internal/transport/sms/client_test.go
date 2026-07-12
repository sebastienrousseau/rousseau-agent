package sms

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestNew_UnknownProviderErrors(t *testing.T) {
	_, err := New(Config{Provider: "aws-sns", From: "+1"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_TwilioRequiresCredentials(t *testing.T) {
	_, err := New(Config{Provider: ProviderTwilio, From: "+1"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_VonageRequiresCredentials(t *testing.T) {
	_, err := New(Config{Provider: ProviderVonage, From: "+1"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_RequiresFrom(t *testing.T) {
	_, err := New(Config{Provider: ProviderTwilio, AccountSID: "AC1", AuthToken: "t"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_TwilioHappyPath(t *testing.T) {
	c, err := New(Config{
		Provider: ProviderTwilio, From: "+15551234567",
		AccountSID: "AC1", AuthToken: "t",
	}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "sms-twilio", c.Name())
}

func TestNew_VonageHappyPath(t *testing.T) {
	c, err := New(Config{
		Provider: ProviderVonage, From: "+15551234567",
		APIKey: "k", AuthToken: "s",
	}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "sms-vonage", c.Name())
}

func TestTwilio_DeliverPostsExpectedForm(t *testing.T) {
	var (
		recordedURL   string
		recordedBody  []byte
		recordedAuth  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedURL = r.URL.Path
		recordedAuth = r.Header.Get("Authorization")
		recordedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sid":"SM1","status":"queued"}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		Provider: ProviderTwilio, From: "+15551234567",
		AccountSID: "AC_TEST", AuthToken: "tok",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "+15559876543", "hi"))

	assert.Contains(t, recordedURL, "/Accounts/AC_TEST/Messages.json")
	assert.Contains(t, recordedAuth, "Basic ")

	form := parseForm(t, recordedBody)
	assert.Equal(t, "+15559876543", form.Get("To"))
	assert.Equal(t, "+15551234567", form.Get("From"))
	assert.Equal(t, "hi", form.Get("Body"))
}

func TestTwilio_ReplyHeaderPrepended(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, err := New(Config{
		Provider: ProviderTwilio, From: "+1", AccountSID: "AC1", AuthToken: "t",
		BaseURL: srv.URL, HTTPClient: srv.Client(), ReplyHeader: "[bot] ",
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "+2", "body"))
	assert.Equal(t, "[bot] body", parseForm(t, recorded).Get("Body"))
}

func TestTwilio_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":20003,"message":"authentication error"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c, err := New(Config{
		Provider: ProviderTwilio, From: "+1", AccountSID: "AC1", AuthToken: "t",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "+2", "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
}

func TestVonage_DeliverPostsExpectedForm(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"status":"0","message-id":"m1"}]}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		Provider: ProviderVonage, From: "+15551234567",
		APIKey: "K", AuthToken: "S",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "+15559876543", "hi"))

	form := parseForm(t, recorded)
	assert.Equal(t, "15559876543", form.Get("to"))     // stripped leading +
	assert.Equal(t, "15551234567", form.Get("from"))
	assert.Equal(t, "hi", form.Get("text"))
	assert.Equal(t, "K", form.Get("api_key"))
	assert.Equal(t, "S", form.Get("api_secret"))
}

func TestVonage_ProviderErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"messages":[{"status":"4","error-text":"Bad credentials"}]}`))
	}))
	defer srv.Close()
	c, err := New(Config{
		Provider: ProviderVonage, From: "+1", APIKey: "K", AuthToken: "S",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "+2", "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Bad credentials")
}

func TestStart_BlocksUntilContextCancel(t *testing.T) {
	c, err := New(Config{
		Provider: ProviderTwilio, From: "+1", AccountSID: "AC1", AuthToken: "t",
	}, silentLogger())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = c.Start(ctx, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestStart_HandlerNilErrors(t *testing.T) {
	c, err := New(Config{
		Provider: ProviderTwilio, From: "+1", AccountSID: "AC1", AuthToken: "t",
	}, silentLogger())
	require.NoError(t, err)
	err = c.Start(context.Background(), nil)
	assert.Error(t, err)
}

func TestStopIdempotent(t *testing.T) {
	c, err := New(Config{
		Provider: ProviderTwilio, From: "+1", AccountSID: "AC1", AuthToken: "t",
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hi", truncate("hi", 10))
	assert.True(t, len(truncate("hello world", 5)) < len("hello world"))
}

// parseForm parses a form body into url.Values for assertions.
func parseForm(t *testing.T, body []byte) formValues {
	t.Helper()
	kv := formValues{}
	for _, pair := range strings.Split(string(body), "&") {
		i := strings.Index(pair, "=")
		if i < 0 {
			continue
		}
		key := urlDecode(pair[:i])
		val := urlDecode(pair[i+1:])
		kv[key] = val
	}
	return kv
}

type formValues map[string]string

func (f formValues) Get(key string) string { return f[key] }

func urlDecode(s string) string {
	// Handle the small subset our test bodies actually produce.
	s = strings.ReplaceAll(s, "+", " ")
	// %XY decode
	out := &strings.Builder{}
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi := hex(s[i+1])
			lo := hex(s[i+2])
			out.WriteByte(byte(hi*16 + lo))
			i += 2
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func hex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return 0
}
