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

// authResultsPass reports whether the receiving MTA authenticated the
// From domain. Only Authentication-Results headers whose authserv-id
// is authservID (our own MTA) count, and only the topmost of them:
// any other instance may have been written by the sender, and RFC 8601
// only obliges an MTA to strip headers carrying its own id. The
// trusted header passes on dmarc=pass for the From domain, or, when it
// carries no DMARC result, on a dkim=pass whose signing domain is the
// From domain or a parent of it. Anything else fails.
func authResultsPass(m *imapclient.FetchMessageBuffer, from, authservID string) bool {
	_, domain, ok := strings.Cut(from, "@")
	if !ok || domain == "" || strings.TrimSpace(authservID) == "" {
		return false
	}
	domain = strings.ToLower(domain)
	for _, line := range authResultsLines(m) {
		id, results := splitAuthservID(line)
		if !strings.EqualFold(id, authservID) {
			continue // not ours: possibly written by the sender
		}
		return resultPasses(results, domain) // the topmost trusted header decides
	}
	return false
}

// splitAuthservID returns the authserv-id of an Authentication-Results
// value (its first token; a trailing version number is dropped) and
// the result clauses after the first ';'.
func splitAuthservID(line string) (string, string) {
	head, rest, _ := strings.Cut(line, ";")
	fields := strings.Fields(head)
	if len(fields) == 0 {
		return "", ""
	}
	return fields[0], rest
}

// resultPasses applies DMARC when the trusted header carries a DMARC
// result for domain, and aligned DKIM otherwise.
func resultPasses(results, domain string) bool {
	if verdict, ok := dmarcResult(results, domain); ok {
		return verdict == "pass"
	}
	return dkimPassFor(results, domain)
}

// dmarcResult finds the dmarc=<verdict> clause whose header.from is
// domain and returns the verdict.
func dmarcResult(results, domain string) (string, bool) {
	for _, clause := range strings.Split(results, ";") {
		fields := strings.Fields(clause)
		if len(fields) == 0 {
			continue
		}
		method, verdict, _ := strings.Cut(strings.ToLower(fields[0]), "=")
		if method != "dmarc" {
			continue
		}
		for _, p := range fields[1:] {
			if key, val, ok := strings.Cut(p, "="); ok && strings.EqualFold(key, "header.from") && strings.EqualFold(val, domain) {
				return verdict, true
			}
		}
	}
	return "", false
}

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
