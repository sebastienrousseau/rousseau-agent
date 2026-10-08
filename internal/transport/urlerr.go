package transport

import (
	"errors"
	"net/url"
	"strings"
)

// redacted replaces a secret in a URL.
const redacted = "***"

// RedactURLError keeps credentials that a client carries in its
// request URL (a bot token in the path, a password in the query) out
// of error messages and logs. net/http reports a failed request as a
// *url.Error whose Error() includes the full URL; for such an error
// (found with errors.As) RedactURLError returns a new *url.Error with
// the same Op and Err and every occurrence of each non-empty secret in
// the URL — raw, path-escaped or query-escaped — replaced by "***".
// Any other error, and nil, is returned unchanged.
func RedactURLError(err error, secrets ...string) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	return &url.Error{Op: ue.Op, URL: redactSecrets(ue.URL, secrets), Err: ue.Err}
}

func redactSecrets(s string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, form := range []string{secret, url.PathEscape(secret), url.QueryEscape(secret)} {
			s = strings.ReplaceAll(s, form, redacted)
		}
	}
	return s
}
