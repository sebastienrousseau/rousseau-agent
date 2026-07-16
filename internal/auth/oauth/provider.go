package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Token is the persisted shape of an OAuth2 credential set. Refresh
// token and Extra are optional (some providers do not return either).
type Token struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	TokenType    string         `json:"token_type,omitempty"`
	Expiry       time.Time      `json:"expiry,omitzero"`
	Extra        map[string]any `json:"extra,omitempty"`
}

// MarshalJSON is provided so a Token round-trips through the encrypted
// store without shape drift when Expiry is the zero value.
func (t Token) MarshalJSON() ([]byte, error) {
	type raw Token
	return json.Marshal(raw(t))
}

// Provider represents a single OAuth2 endpoint (Google, GitHub,
// Slack, Linear). Implementations must be safe for concurrent use.
type Provider interface {
	// Name is the stable identifier used as the primary-key prefix in
	// the token store ("google", "github", "slack", "linear").
	Name() string
	// AuthCodeURL builds the browser URL the operator visits to
	// authorise. state must be embedded verbatim in the callback URL
	// and is used for CSRF protection.
	AuthCodeURL(state string) string
	// Exchange trades a callback code for an access/refresh token.
	Exchange(ctx context.Context, code string) (*Token, error)
	// Refresh trades a refresh token for a fresh access token. Some
	// providers issue a new refresh token here; if so, callers should
	// persist the returned Token verbatim.
	Refresh(ctx context.Context, refreshToken string) (*Token, error)
	// Client returns an *http.Client that automatically injects the
	// current token on every request and refreshes when the token
	// is close to expiry. The lifetime is bound to ctx.
	Client(ctx context.Context, tok *Token) (*http.Client, error)
}
