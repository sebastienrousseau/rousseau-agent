package oauth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests drive Broker.Serve end-to-end over a real loopback
// listener and a real HTTP redirect, covering the callback handler's
// rejection paths (bad state, provider-reported error, missing code,
// exchange failure) plus Serve's own setup and shutdown branches.

// freeTCPAddr returns a loopback address that was free a moment ago.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// stateFromAuthURL extracts the state fakeProvider embedded in the URL
// it handed back to the operator.
func stateFromAuthURL(t *testing.T, authURL string) string {
	t.Helper()
	parts := strings.SplitN(authURL, "state=", 2)
	require.Len(t, parts, 2, "auth URL %q carries no state", authURL)
	return strings.SplitN(parts[1], "&", 2)[0]
}

// fireCallback plays the role of the operator's browser being
// redirected back to the local listener.
func fireCallback(addr, provider string, q url.Values) {
	target := "http://" + addr + "/oauth/callback/" + provider + "?" + q.Encode()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(target) //nolint:gosec,noctx // loopback test fixture
		if err == nil {
			_ = resp.Body.Close() //nolint:errcheck // best-effort close
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// serveWithRedirect runs Serve against a broker bound to a free port,
// firing a callback whose query string is built by makeQuery once the
// listener is live.
func serveWithRedirect(t *testing.T, b *Broker, provider string, makeQuery func(state string) url.Values) (*Token, error) {
	t.Helper()
	addr := freeTCPAddr(t)
	b.CallbackAddr = addr
	if b.CallbackTimeout == 0 {
		b.CallbackTimeout = 5 * time.Second
	}
	open := func(authURL string) error {
		go fireCallback(addr, provider, makeQuery(stateFromAuthURL(t, authURL)))
		return nil
	}
	return b.Serve(context.Background(), provider, "alice", open, silentLogger())
}

// brokerWithProvider builds a broker whose single provider exchanges
// codes using exchange.
func brokerWithProvider(t *testing.T, exchange func(context.Context, string) (*Token, error)) *Broker {
	t.Helper()
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{name: "google", authURL: "http://provider.local/authorize", exchange: exchange})
	return b
}

func okExchange(context.Context, string) (*Token, error) {
	return &Token{AccessToken: "at", RefreshToken: "rt"}, nil
}

func TestServe_RejectsMismatchedState(t *testing.T) {
	b := brokerWithProvider(t, okExchange)
	_, err := serveWithRedirect(t, b, "google", func(string) url.Values {
		return url.Values{"state": {"forged-state"}, "code": {"code-xyz"}}
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "state mismatch")
}

func TestServe_SurfacesProviderReportedError(t *testing.T) {
	b := brokerWithProvider(t, okExchange)
	_, err := serveWithRedirect(t, b, "google", func(state string) url.Values {
		return url.Values{
			"state":             {state},
			"error":             {"access_denied"},
			"error_description": {"the operator declined"},
		}
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "access_denied")
	assert.ErrorContains(t, err, "the operator declined")
}

func TestServe_RejectsCallbackWithoutCode(t *testing.T) {
	b := brokerWithProvider(t, okExchange)
	_, err := serveWithRedirect(t, b, "google", func(state string) url.Values {
		return url.Values{"state": {state}}
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "missing code")
}

func TestServe_SurfacesExchangeFailure(t *testing.T) {
	b := brokerWithProvider(t, func(context.Context, string) (*Token, error) {
		return nil, errors.New("token endpoint said no")
	})
	_, err := serveWithRedirect(t, b, "google", func(state string) url.Values {
		return url.Values{"state": {state}, "code": {"code-xyz"}}
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "token endpoint said no")
}

// TestServe_BrowserLaunchFailureIsNonFatal proves a headless box (no
// browser to open) still completes the flow — the operator can paste
// the URL themselves.
func TestServe_BrowserLaunchFailureIsNonFatal(t *testing.T) {
	b := brokerWithProvider(t, okExchange)
	addr := freeTCPAddr(t)
	b.CallbackAddr = addr
	b.CallbackTimeout = 5 * time.Second

	open := func(authURL string) error {
		state := stateFromAuthURL(t, authURL)
		go fireCallback(addr, "google", url.Values{"state": {state}, "code": {"code-xyz"}})
		return errors.New("no display available")
	}
	tok, err := b.Serve(context.Background(), "google", "alice", open, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "at", tok.AccessToken)
}

func TestServe_UnknownProviderFailsBeforeBinding(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	_, err := b.Serve(context.Background(), "nope", "alice",
		func(string) error { return nil }, silentLogger())
	require.Error(t, err)
	assert.ErrorContains(t, err, "unknown provider")
}

func TestServe_BindFailureIsReported(t *testing.T) {
	b := brokerWithProvider(t, okExchange)
	// Hold the port so Serve cannot bind it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }() //nolint:errcheck // best-effort close
	b.CallbackAddr = l.Addr().String()
	b.CallbackTimeout = time.Second

	_, err = b.Serve(context.Background(), "google", "alice",
		func(string) error { return nil }, silentLogger())
	require.Error(t, err)
	assert.ErrorContains(t, err, "bind callback")
}

// TestServe_NilLoggerAndNilBrowser covers the defaults: a nil logger
// falls back to slog.Default and a nil openBrowser simply skips the
// launch, leaving the flow to time out.
func TestServe_NilLoggerAndNilBrowser(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	b := brokerWithProvider(t, okExchange)
	b.CallbackAddr = freeTCPAddr(t)
	b.CallbackTimeout = 50 * time.Millisecond

	_, err := b.Serve(context.Background(), "google", "alice", nil, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "callback timeout")
}

func TestServe_ContextCancellationAborts(t *testing.T) {
	b := brokerWithProvider(t, okExchange)
	b.CallbackAddr = freeTCPAddr(t)
	b.CallbackTimeout = 30 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := b.Serve(ctx, "google", "alice", func(string) error { return nil }, silentLogger())
	assert.ErrorIs(t, err, context.Canceled)
}

// TestStateURL_BuildsCallbackURL documents the helper providers'
// redirects are compared against.
func TestStateURL_BuildsCallbackURL(t *testing.T) {
	got := StateURL("127.0.0.1:8765", "google", "st4te", "c0de")
	u, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:8765", u.Host)
	assert.Equal(t, "/oauth/callback/google", u.Path)
	assert.Equal(t, "st4te", u.Query().Get("state"))
	assert.Equal(t, "c0de", u.Query().Get("code"))
}
