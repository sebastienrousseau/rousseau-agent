package vertex

import (
	"context"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// oauth2Client wraps ADC-derived credentials as an *http.Client. Kept
// in its own file so client_test.go can inject fakes via
// Config.HTTPClient without depending on the oauth2 machinery.
func oauth2Client(ctx context.Context, creds *google.Credentials) *http.Client {
	return oauth2.NewClient(ctx, creds.TokenSource)
}

// readAllFile is a tiny indirection kept for testability. os.ReadFile
// is called from New() when Config.CredentialsFile is set.
func readAllFile(path string) ([]byte, error) { return os.ReadFile(path) }
