package oauth

import (
	"context"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
)

// oauth2Provider adapts a golang.org/x/oauth2.Config to the Provider
// interface. Every well-known provider (Google, GitHub, Slack, Linear)
// exports a *oauth2.Config; wrapping it lets us share the exchange /
// refresh / auto-refresh HTTP client logic across all of them.
type oauth2Provider struct {
	name string
	cfg  *oauth2.Config
}

// NewOAuth2Provider wraps the supplied [oauth2.Config] as a Provider.
// name is the stable identifier used in the token store.
func NewOAuth2Provider(name string, cfg *oauth2.Config) Provider {
	return &oauth2Provider{name: name, cfg: cfg}
}

func (p *oauth2Provider) Name() string { return p.name }

func (p *oauth2Provider) AuthCodeURL(state string) string {
	// AccessTypeOffline requests a refresh token where the provider
	// supports the notion (Google requires it; GitHub ignores it and
	// always issues refresh tokens on token exchange when eligible).
	return p.cfg.AuthCodeURL(state, oauth2.AccessTypeOffline)
}

func (p *oauth2Provider) Exchange(ctx context.Context, code string) (*Token, error) {
	tok, err := p.cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth: exchange (%s): %w", p.name, err)
	}
	return fromOAuth2(tok), nil
}

func (p *oauth2Provider) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	src := p.cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	tok, err := src.Token()
	if err != nil {
		return nil, fmt.Errorf("oauth: refresh (%s): %w", p.name, err)
	}
	return fromOAuth2(tok), nil
}

func (p *oauth2Provider) Client(ctx context.Context, tok *Token) (*http.Client, error) {
	if tok == nil {
		return nil, fmt.Errorf("oauth: nil token")
	}
	src := p.cfg.TokenSource(ctx, toOAuth2(tok))
	return oauth2.NewClient(ctx, src), nil
}

func fromOAuth2(tok *oauth2.Token) *Token {
	if tok == nil {
		return nil
	}
	return &Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
		Expiry:       tok.Expiry,
	}
}

func toOAuth2(tok *Token) *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
		Expiry:       tok.Expiry,
	}
}
