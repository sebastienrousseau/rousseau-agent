package oauth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider satisfies Provider with configurable responses.
type fakeProvider struct {
	name      string
	authURL   string
	exchange  func(ctx context.Context, code string) (*Token, error)
	refresh   func(ctx context.Context, rt string) (*Token, error)
	clientErr error
}

func (f *fakeProvider) Name() string                    { return f.name }
func (f *fakeProvider) AuthCodeURL(state string) string { return f.authURL + "?state=" + state }
func (f *fakeProvider) Exchange(ctx context.Context, code string) (*Token, error) {
	return f.exchange(ctx, code)
}
func (f *fakeProvider) Refresh(ctx context.Context, rt string) (*Token, error) {
	return f.refresh(ctx, rt)
}
func (f *fakeProvider) Client(context.Context, *Token) (*http.Client, error) {
	if f.clientErr != nil {
		return nil, f.clientErr
	}
	return http.DefaultClient, nil
}

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBroker_StartMintsUniqueStates(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{name: "google", authURL: "http://x"})

	_, s1, err := b.Start("google")
	require.NoError(t, err)
	_, s2, err := b.Start("google")
	require.NoError(t, err)
	assert.NotEqual(t, s1, s2)
}

func TestBroker_StartRejectsUnknownProvider(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	_, _, err := b.Start("nope")
	assert.Error(t, err)
}

func TestBroker_CompletePersistsToken(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{
		name:    "google",
		authURL: "http://x",
		exchange: func(context.Context, string) (*Token, error) {
			return &Token{AccessToken: "at", RefreshToken: "rt"}, nil
		},
	})

	_, state, err := b.Start("google")
	require.NoError(t, err)
	tok, err := b.Complete(context.Background(), state, "code-xyz", "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "at", tok.AccessToken)

	// Round-trip: Load returns the persisted row.
	got, err := b.Load(context.Background(), "google", "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "at", got.AccessToken)
}

func TestBroker_CompleteRejectsUnknownState(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{name: "google"})
	_, err := b.Complete(context.Background(), "not-a-state", "code", "acct")
	assert.Error(t, err)
}

func TestBroker_LoadRefreshesNearExpiry(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	refreshed := 0
	b.Register(&fakeProvider{
		name: "google",
		refresh: func(context.Context, string) (*Token, error) {
			refreshed++
			return &Token{AccessToken: "at-fresh", Expiry: time.Now().Add(time.Hour)}, nil
		},
	})
	// Seed a token that's already past the refresh window.
	require.NoError(t, v.Put(context.Background(), "google", "acct", &Token{
		AccessToken: "at-old", RefreshToken: "rt", Expiry: time.Now().Add(-time.Minute),
	}))
	got, err := b.Load(context.Background(), "google", "acct")
	require.NoError(t, err)
	assert.Equal(t, "at-fresh", got.AccessToken)
	assert.Equal(t, 1, refreshed)
}

func TestBroker_LoadPreservesRefreshTokenOnRefresh(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{
		name: "google",
		refresh: func(context.Context, string) (*Token, error) {
			// Providers commonly omit refresh_token on refresh.
			return &Token{AccessToken: "at-new", Expiry: time.Now().Add(time.Hour)}, nil
		},
	})
	require.NoError(t, v.Put(context.Background(), "google", "acct", &Token{
		AccessToken: "at-old", RefreshToken: "rt-keepme", Expiry: time.Now().Add(-time.Minute),
	}))
	got, err := b.Load(context.Background(), "google", "acct")
	require.NoError(t, err)
	assert.Equal(t, "rt-keepme", got.RefreshToken)
}

func TestBroker_LoadReturnsErrorForMissing(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{name: "google"})
	_, err := b.Load(context.Background(), "google", "nobody")
	assert.Error(t, err)
}

func TestBroker_RevokeDeletes(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{name: "google"})
	require.NoError(t, v.Put(context.Background(), "google", "acct", &Token{AccessToken: "at"}))
	require.NoError(t, b.Revoke(context.Background(), "google", "acct"))
	_, err := b.Load(context.Background(), "google", "acct")
	assert.Error(t, err)
}

// TestServe_RoundTrip stands up the callback server and simulates a
// provider redirect. Verifies the token is persisted end-to-end.
func TestServe_RoundTrip(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{
		name:    "google",
		authURL: "http://provider.local/authorize",
		exchange: func(context.Context, string) (*Token, error) {
			return &Token{AccessToken: "at", RefreshToken: "rt"}, nil
		},
	})

	// Bind a random free port so parallel tests don't collide.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	b.CallbackAddr = addr
	b.CallbackTimeout = 2 * time.Second

	openBrowser := func(url string) error {
		// The real broker generates state internally. Fish it out of the
		// authURL query string, then fire the callback in the background
		// so Serve's select picks it up.
		state := strings.SplitN(strings.SplitN(url, "state=", 2)[1], "&", 2)[0]
		go func() {
			// Wait for the server to be ready, then trigger the callback.
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				resp, err := http.Get(StateURL(addr, "google", state, "code-xyz"))
				if err == nil {
					_ = resp.Body.Close() // ignore close error on success path
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
		return nil
	}

	tok, err := b.Serve(context.Background(), "google", "acct", openBrowser, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "at", tok.AccessToken)

	// Persisted.
	stored, err := b.Load(context.Background(), "google", "acct")
	require.NoError(t, err)
	assert.Equal(t, "at", stored.AccessToken)
}

func TestServe_TimeoutReturnsError(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{
		name: "google", authURL: "http://x",
		exchange: func(context.Context, string) (*Token, error) {
			return nil, errors.New("never called")
		},
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	b.CallbackAddr = addr
	b.CallbackTimeout = 50 * time.Millisecond

	_, err = b.Serve(context.Background(), "google", "acct",
		func(string) error { return nil }, silentLogger())
	assert.ErrorContains(t, err, "timeout")
}
