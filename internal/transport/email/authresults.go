package email

import (
	"bufio"
	"bytes"
	"net/textproto"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func bufioReader(b []byte) *bufio.Reader { return bufio.NewReader(bytes.NewReader(b)) }

// authResultsHeader is the RFC 8601 header the receiving MTA adds
// with the outcome of its SPF / DKIM / DMARC checks.
const authResultsHeader = "Authentication-Results"

func isHeaderSection(s *imap.FetchItemBodySection) bool {
	return s != nil && s.Specifier == imap.PartSpecifierHeader
}

// authResultsPass reports whether the message carries an
// Authentication-Results header with dkim=pass whose signing domain
// (header.d= or header.i=) matches, or is a parent of, the From
// address's domain. A message with no such header fails: the MTA
// either did not authenticate it or does not add the header, and in
// both cases the From address proves nothing.
func authResultsPass(m *imapclient.FetchMessageBuffer, from string) bool {
	_, domain, ok := strings.Cut(from, "@")
	if !ok || domain == "" {
		return false
	}
	for _, line := range authResultsLines(m) {
		if dkimPassFor(line, strings.ToLower(domain)) {
			return true
		}
	}
	return false
}

// authResultsLines extracts every Authentication-Results value from
// the header section(s) of the fetched message.
func authResultsLines(m *imapclient.FetchMessageBuffer) []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, section := range m.BodySection {
		if !isHeaderSection(section.Section) || len(section.Bytes) == 0 {
			continue
		}
		r := textproto.NewReader(bufioReader(section.Bytes))
		hdr, err := r.ReadMIMEHeader()
		if err != nil && len(hdr) == 0 {
			continue
		}
		out = append(out, hdr.Values(authResultsHeader)...)
	}
	return out
}

// dkimPassFor parses one Authentication-Results value, e.g.
//
//	mx.example.net; dkim=pass header.d=corp.com header.s=sel; spf=pass
//
// and reports whether a dkim=pass clause names domain (or a parent
// domain, so header.d=corp.com covers ceo@mail.corp.com).
func dkimPassFor(line, domain string) bool {
	for _, clause := range strings.Split(line, ";") {
		fields := strings.Fields(clause)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "dkim=pass") {
			continue
		}
		if clauseSignsDomain(fields[1:], domain) {
			return true
		}
	}
	return false
}

// clauseSignsDomain reports whether a dkim=pass clause's properties
// name domain or a parent of it.
func clauseSignsDomain(props []string, domain string) bool {
	for _, p := range props {
		key, val, ok := strings.Cut(p, "=")
		if !ok || (key != "header.d" && key != "header.i") {
			continue
		}
		if signer := signingDomain(key, val); signer == domain || strings.HasSuffix(domain, "."+signer) {
			return true
		}
	}
	return false
}

// signingDomain normalises header.d=corp.com / header.i=@corp.com /
// header.i=user@corp.com to "corp.com".
func signingDomain(key, val string) string {
	val = strings.ToLower(strings.TrimPrefix(val, "@"))
	if key == "header.i" {
		if _, d, ok := strings.Cut(val, "@"); ok {
			return d
		}
	}
	return val
}
