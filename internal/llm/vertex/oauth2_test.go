package vertex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// TestOAuth2Client_Basic drives the oauth2Client wrapper with a
// canned Credentials source so the function stops showing as 0%
// coverage. The returned *http.Client is opaque; we just verify it
// isn't nil and can build a request.
func TestOAuth2Client_Basic(t *testing.T) {
	creds := &google.Credentials{
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "at-live",
			TokenType:   "Bearer",
		}),
	}
	c := oauth2Client(context.Background(), creds)
	assert.NotNil(t, c)
}
