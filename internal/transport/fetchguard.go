package transport

import (
	"regexp"
	"strings"
)

// FetchPattern matches URLs a transport may fetch on a payload's behalf:
// exactly scheme, then exactly one of hosts, then a path. Anything else
// fails to match, including a port or userinfo the host list does not
// name, a look-alike suffix, and a differently cased scheme or host.
//
// Callers check it inline (if !p.MatchString(url) { refuse }) right
// before building the request. A regexp match on the URL itself is a
// check CodeQL's request-forgery query recognises; a helper that parses
// and compares fields is not, so the alert stays open even though the
// code is safe.
func FetchPattern(scheme string, hosts ...string) *regexp.Regexp {
	quoted := make([]string, len(hosts))
	for i, h := range hosts {
		quoted[i] = regexp.QuoteMeta(h)
	}
	return regexp.MustCompile(`^` + regexp.QuoteMeta(scheme) + `://(?:` + strings.Join(quoted, "|") + `)/`)
}
