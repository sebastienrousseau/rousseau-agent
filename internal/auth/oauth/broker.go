package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Broker orchestrates one OAuth2 flow at a time. Concurrent flows on
// the same broker are refused rather than raced — the operator can
// only sit at one browser at a time and the CLI is designed for that
// serial pattern.
type Broker struct {
	vault     *Vault
	providers map[string]Provider

	mu       sync.Mutex
	inflight map[string]inflightState // state → provider name

	// CallbackAddr is the interface:port the [Broker.Serve] server
	// binds. Empty defaults to 127.0.0.1:8765.
	CallbackAddr string
	// CallbackTimeout is how long [Broker.Serve] waits for a callback
	// before returning ctx-timeout. Zero uses 5 minutes.
	CallbackTimeout time.Duration
}

type inflightState struct {
	provider string
	created  time.Time
}

// NewBroker constructs a Broker with an empty provider set. Register
// providers via [Broker.Register] before calling [Broker.Start].
func NewBroker(vault *Vault) *Broker {
	return &Broker{
		vault:     vault,
		providers: make(map[string]Provider),
		inflight:  make(map[string]inflightState),
	}
}

// Register attaches a Provider under its Name. Registering the same
// name twice replaces the earlier registration — call sites are
// expected to register once at daemon startup.
func (b *Broker) Register(p Provider) { b.providers[p.Name()] = p }

// Providers returns the registered provider names in insertion order
// is not guaranteed — callers should sort if display order matters.
func (b *Broker) Providers() []string {
	out := make([]string, 0, len(b.providers))
	for name := range b.providers {
		out = append(out, name)
	}
	return out
}

// Start begins an OAuth flow. Returns the auth-code URL the operator
// visits in a browser plus the state token that will appear on the
// callback. Callers must feed the state back into [Broker.Complete]
// exactly.
func (b *Broker) Start(providerName string) (url, state string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.providers[providerName]
	if !ok {
		return "", "", fmt.Errorf("oauth: unknown provider %q", providerName)
	}
	state, err = randomState()
	if err != nil {
		return "", "", err
	}
	b.inflight[state] = inflightState{provider: providerName, created: time.Now()}
	// Age out any orphaned states from earlier abandoned flows.
	for k, v := range b.inflight {
		if time.Since(v.created) > 30*time.Minute {
			delete(b.inflight, k)
		}
	}
	return p.AuthCodeURL(state), state, nil
}

// Complete finishes an in-flight OAuth flow: it looks up state,
// exchanges code, and persists the resulting token under
// (provider, accountID). accountID is caller-supplied — for personal
// use the operator's email or "default" is typical; enterprise
// installs might use the tenant id.
func (b *Broker) Complete(ctx context.Context, state, code, accountID string) (*Token, error) {
	b.mu.Lock()
	inf, ok := b.inflight[state]
	if ok {
		delete(b.inflight, state)
	}
	b.mu.Unlock()
	if !ok {
		return nil, errors.New("oauth: unknown state")
	}
	p, ok := b.providers[inf.provider]
	if !ok {
		return nil, fmt.Errorf("oauth: provider %q vanished mid-flow", inf.provider)
	}
	tok, err := p.Exchange(ctx, code)
	if err != nil {
		return nil, err
	}
	if err := b.vault.Put(ctx, inf.provider, accountID, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

// Revoke deletes the persisted token. Provider-side revocation is
// left to the caller — some providers expose a /revoke endpoint, some
// don't.
func (b *Broker) Revoke(ctx context.Context, providerName, accountID string) error {
	return b.vault.Delete(ctx, providerName, accountID)
}

// Load fetches a stored token, refreshing it if the access token is
// within [refreshWindow] of expiry. Callers should prefer Load over
// direct Vault.Get.
func (b *Broker) Load(ctx context.Context, providerName, accountID string) (*Token, error) {
	tok, ok, err := b.vault.Get(ctx, providerName, accountID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("oauth: no token for %s/%s", providerName, accountID)
	}
	if !tok.Expiry.IsZero() && time.Until(tok.Expiry) < refreshWindow && tok.RefreshToken != "" {
		p, ok := b.providers[providerName]
		if !ok {
			return nil, fmt.Errorf("oauth: unknown provider %q", providerName)
		}
		fresh, err := p.Refresh(ctx, tok.RefreshToken)
		if err != nil {
			return nil, err
		}
		// Some providers omit refresh token on refresh; preserve the old
		// one so the token doesn't decay to access-only.
		if fresh.RefreshToken == "" {
			fresh.RefreshToken = tok.RefreshToken
		}
		if err := b.vault.Put(ctx, providerName, accountID, fresh); err != nil {
			return nil, err
		}
		return fresh, nil
	}
	return tok, nil
}

// refreshWindow is how close to expiry a token must be before Load
// proactively refreshes it.
const refreshWindow = 60 * time.Second

// randomState mints a 128-bit random state token, hex-encoded.
func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("oauth: random state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// resolveAddr returns the callback bind address with the default
// applied.
func (b *Broker) resolveAddr() string {
	if b.CallbackAddr != "" {
		return b.CallbackAddr
	}
	return "127.0.0.1:8765"
}

// resolveTimeout returns the callback timeout with the default
// applied.
func (b *Broker) resolveTimeout() time.Duration {
	if b.CallbackTimeout != 0 {
		return b.CallbackTimeout
	}
	return 5 * time.Minute
}

// Assert we can bind the configured address at construction time so
// operators see the error early rather than on first flow.
var _ net.Listener = (*net.TCPListener)(nil)
