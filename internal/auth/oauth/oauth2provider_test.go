package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// oauthMockServer mints valid OAuth2 endpoints in-process so the
// oauth2provider wrapper's Exchange / Refresh paths can be exercised
// end-to-end without hitting a real IdP.
func oauthMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			require.NoError(t, r.ParseForm())
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			_, _ = w.Write([]byte(url.Values{ //nolint:errcheck // test fixture
				"access_token":  {"at-live"},
				"refresh_token": {"rt-live"},
				"token_type":    {"Bearer"},
				"expires_in":    {"3600"},
			}.Encode()))
		case "/protected":
			auth := r.Header.Get("Authorization")
			if auth != "Bearer at-live" {
				http.Error(w, "unauth", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test fixture
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mockCfg(srv *httptest.Server) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     "cid",
		ClientSecret: "csecret",
		Endpoint: oauth2.Endpoint{
			AuthURL:  srv.URL + "/authorize",
			TokenURL: srv.URL + "/token",
		},
		RedirectURL: "http://127.0.0.1:8765/oauth/callback/test",
		Scopes:      []string{"read"},
	}
}

func TestNewOAuth2Provider_Basics(t *testing.T) {
	srv := oauthMockServer(t)
	p := NewOAuth2Provider("test", mockCfg(srv))
	assert.Equal(t, "test", p.Name())

	u := p.AuthCodeURL("state-xyz")
	assert.Contains(t, u, "state=state-xyz")
	assert.Contains(t, u, "access_type=offline")
	assert.Contains(t, u, "client_id=cid")
}

func TestOAuth2Provider_ExchangeRoundTrip(t *testing.T) {
	srv := oauthMockServer(t)
	p := NewOAuth2Provider("test", mockCfg(srv))
	tok, err := p.Exchange(context.Background(), "code-xyz")
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.Equal(t, "at-live", tok.AccessToken)
	assert.Equal(t, "rt-live", tok.RefreshToken)
	assert.Equal(t, "Bearer", tok.TokenType)
	assert.False(t, tok.Expiry.IsZero())
}

func TestOAuth2Provider_ExchangeErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	p := NewOAuth2Provider("test", mockCfg(srv))
	_, err := p.Exchange(context.Background(), "bad")
	assert.ErrorContains(t, err, "exchange")
}

func TestOAuth2Provider_Refresh(t *testing.T) {
	srv := oauthMockServer(t)
	p := NewOAuth2Provider("test", mockCfg(srv))
	tok, err := p.Refresh(context.Background(), "rt-stored")
	require.NoError(t, err)
	assert.Equal(t, "at-live", tok.AccessToken)
}

func TestOAuth2Provider_ClientAutoAuthorizes(t *testing.T) {
	srv := oauthMockServer(t)
	p := NewOAuth2Provider("test", mockCfg(srv))
	tok := &Token{AccessToken: "at-live", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	c, err := p.Client(context.Background(), tok)
	require.NoError(t, err)
	resp, err := c.Get(srv.URL + "/protected")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestOAuth2Provider_ClientNilTokenRejected(t *testing.T) {
	srv := oauthMockServer(t)
	p := NewOAuth2Provider("test", mockCfg(srv))
	_, err := p.Client(context.Background(), nil)
	assert.Error(t, err)
}

func TestFromToOAuth2_RoundTrip(t *testing.T) {
	src := &oauth2.Token{
		AccessToken: "a", RefreshToken: "r", TokenType: "Bearer",
		Expiry: time.Unix(1_700_000_000, 0),
	}
	got := fromOAuth2(src)
	assert.Equal(t, src.AccessToken, got.AccessToken)
	assert.Equal(t, src.RefreshToken, got.RefreshToken)
	assert.Equal(t, src.TokenType, got.TokenType)
	assert.Equal(t, src.Expiry, got.Expiry)

	round := toOAuth2(got)
	assert.Equal(t, src.AccessToken, round.AccessToken)
	assert.Equal(t, src.Expiry, round.Expiry)
}

func TestFromOAuth2_NilIsNil(t *testing.T) {
	assert.Nil(t, fromOAuth2(nil))
}

func TestProviders_ReturnsNames(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	assert.Empty(t, b.Providers())
	b.Register(&fakeProvider{name: "one"})
	b.Register(&fakeProvider{name: "two"})
	assert.Len(t, b.Providers(), 2)
}
