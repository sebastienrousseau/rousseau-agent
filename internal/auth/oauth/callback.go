package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"
)

// CallbackResult carries the outcome of a single OAuth flow. Exactly
// one of Token / Err is non-nil.
type CallbackResult struct {
	Provider  string
	AccountID string
	Token     *Token
	Err       error
}

// Serve stands up the callback HTTP listener, opens the auth URL in
// the operator's browser (see [OpenBrowser]), and blocks until either
// the callback fires or [Broker.CallbackTimeout] elapses.
//
// The listener is bound to localhost only. Callers should treat the
// returned Token as sensitive and pass it directly to the Broker for
// persistence — this method does not persist on the operator's
// behalf; use [Broker.Complete] via the OAuth flow instead.
//
// This method is a convenience for the CLI: for programmatic tests
// exercise [Broker.Start] + [Broker.Complete] directly.
func (b *Broker) Serve(ctx context.Context, providerName, accountID string, openBrowser func(url string) error, logger *slog.Logger) (*Token, error) {
	if logger == nil {
		logger = slog.Default()
	}
	authURL, state, err := b.Start(providerName)
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", b.resolveAddr())
	if err != nil {
		return nil, fmt.Errorf("oauth: bind callback: %w", err)
	}
	defer func() { _ = ln.Close() }() //nolint:errcheck // best-effort close

	resultC := make(chan CallbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback/"+providerName, func(w http.ResponseWriter, r *http.Request) {
		gotState := r.URL.Query().Get("state")
		if gotState != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resultC <- CallbackResult{Err: errors.New("oauth: state mismatch on callback")}
			return
		}
		if oerr := r.URL.Query().Get("error"); oerr != "" {
			desc := r.URL.Query().Get("error_description")
			http.Error(w, oerr+": "+desc, http.StatusBadRequest)
			resultC <- CallbackResult{Err: fmt.Errorf("oauth: %s: %s", oerr, desc)}
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resultC <- CallbackResult{Err: errors.New("oauth: missing code on callback")}
			return
		}
		tok, err := b.Complete(r.Context(), state, code, accountID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			resultC <- CallbackResult{Err: err}
			return
		}
		_, _ = w.Write([]byte(callbackSuccessHTML)) //nolint:errcheck // best-effort UI
		resultC <- CallbackResult{Provider: providerName, AccountID: accountID, Token: tok}
	})

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }() //nolint:errcheck // Shutdown drives the exit
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) //nolint:errcheck // best-effort shutdown
	}()

	logger.Info("oauth.await_callback", slog.String("provider", providerName), slog.String("url", authURL))
	if openBrowser != nil {
		if err := openBrowser(authURL); err != nil {
			logger.Warn("oauth.open_browser_failed", slog.String("err", err.Error()))
		}
	}

	select {
	case res := <-resultC:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Token, nil
	case <-time.After(b.resolveTimeout()):
		return nil, errors.New("oauth: callback timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// callbackSuccessHTML is the tiny page the operator sees after the
// browser redirects back. Keeps them from wondering if it worked.
const callbackSuccessHTML = `<!doctype html>
<title>rousseau-agent</title>
<style>body{font:16px/1.5 system-ui;padding:2rem;color:#0a0}</style>
<h1>✓ Authorised</h1>
<p>You can close this tab and return to the terminal.</p>`

// StateURL is a small helper used by tests to construct the exact
// callback URL a provider would redirect the operator to.
func StateURL(host, providerName, state, code string) string {
	u := &url.URL{
		Scheme:   "http",
		Host:     host,
		Path:     "/oauth/callback/" + providerName,
		RawQuery: url.Values{"state": {state}, "code": {code}}.Encode(),
	}
	return u.String()
}
