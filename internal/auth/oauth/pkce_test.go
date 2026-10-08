package oauth

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// L-18: the loopback flow uses PKCE (S256), survives a forged-state
// request, never blocks a handler on a result nobody reads, and does
// not log the auth URL (which carries the state).

func TestOAuth2Provider_SendsPKCEChallengeAndVerifier(t *testing.T) {
	var (
		mu           sync.Mutex
		seenVerifier string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		mu.Lock()
		seenVerifier = r.PostForm.Get("code_verifier")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte(url.Values{ //nolint:errcheck // test fixture
			"access_token": {"at-fake"}, "token_type": {"Bearer"},
		}.Encode()))
	}))
	t.Cleanup(srv.Close)
	p := NewOAuth2Provider("test", mockCfg(srv))

	verifier := oauth2.GenerateVerifier()
	u, err := url.Parse(p.AuthCodeURL("state-xyz", verifier))
	require.NoError(t, err)
	assert.Equal(t, "S256", u.Query().Get("code_challenge_method"))
	assert.Equal(t, oauth2.S256ChallengeFromVerifier(verifier), u.Query().Get("code_challenge"))

	_, err = p.Exchange(context.Background(), "code-xyz", verifier)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, verifier, seenVerifier, "token request must carry the PKCE verifier")
}

func TestBroker_ThreadsOneVerifierPerFlow(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	fp := &fakeProvider{name: "google", authURL: "http://x", exchange: okExchange}
	b.Register(fp)

	_, state, err := b.Start("google")
	require.NoError(t, err)
	_, err = b.Complete(context.Background(), state, "code", "alice")
	require.NoError(t, err)
	require.Len(t, fp.verifiers, 2)
	assert.NotEmpty(t, fp.verifiers[0], "Start must mint a PKCE verifier")
	assert.Equal(t, fp.verifiers[0], fp.verifiers[1], "Complete must present the Start verifier")

	_, _, err = b.Start("google")
	require.NoError(t, err)
	assert.NotEqual(t, fp.verifiers[0], fp.verifiers[2], "each flow gets a fresh verifier")
}

// getStatus issues one callback request and returns the status code,
// retrying until the listener is up.
func getStatus(addr, provider string, q url.Values) int {
	target := "http://" + addr + "/oauth/callback/" + provider + "?" + q.Encode()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(target) //nolint:gosec,noctx // loopback test fixture
		if err == nil {
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0
}

func TestServe_ForgedStateIsRejectedButFlowKeepsWaiting(t *testing.T) {
	b := brokerWithProvider(t, okExchange)
	addr := freeTCPAddr(t)
	b.CallbackAddr = addr
	b.CallbackTimeout = 5 * time.Second

	forgedStatus := make(chan int, 1)
	open := func(authURL string) error {
		state := stateFromAuthURL(t, authURL)
		go func() {
			forgedStatus <- getStatus(addr, "google", url.Values{"state": {"forged"}, "code": {"evil"}})
			fireCallback(addr, "google", url.Values{"state": {state}, "code": {"code-xyz"}})
		}()
		return nil
	}
	tok, err := b.Serve(context.Background(), "google", "alice", open, silentLogger())
	require.NoError(t, err, "a forged-state request must not abort the flow")
	assert.Equal(t, "at", tok.AccessToken)
	assert.Equal(t, http.StatusBadRequest, <-forgedStatus)
}

func TestDeliverResult_NeverBlocksOnAFullChannel(t *testing.T) {
	ch := make(chan CallbackResult, 1)
	ch <- CallbackResult{Provider: "first"}
	done := make(chan struct{})
	go func() {
		deliverResult(ch, CallbackResult{Provider: "second"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deliverResult blocked on a full channel")
	}
	assert.Equal(t, "first", (<-ch).Provider, "the first result wins")
}

func TestServe_LogsProviderNotAuthURL(t *testing.T) {
	b := brokerWithProvider(t, okExchange)
	b.CallbackAddr = freeTCPAddr(t)
	b.CallbackTimeout = 50 * time.Millisecond

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	_, _ = b.Serve(context.Background(), "google", "alice", nil, logger) //nolint:errcheck // timeout expected
	out := buf.String()
	assert.Contains(t, out, "provider=google")
	assert.NotContains(t, out, "provider.local/authorize", "auth URL must not be logged")
	assert.NotContains(t, out, "state=", "the CSRF state must not be logged")
}
