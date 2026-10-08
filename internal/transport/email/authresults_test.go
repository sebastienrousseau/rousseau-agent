package email

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/assert"
)

func headerMsg(raw string) *imapclient.FetchMessageBuffer {
	return &imapclient.FetchMessageBuffer{BodySection: []imapclient.FetchBodySectionBuffer{
		{Section: &imap.FetchItemBodySection{}, Bytes: []byte("Subject: x\r\n\r\nbody")},
		{Section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader}, Bytes: []byte(raw)},
	}}
}

func TestAuthResultsPass(t *testing.T) {
	cases := []struct {
		name string
		hdr  string
		from string
		want bool
	}{
		{"dkim pass aligned", "Authentication-Results: mx.net; dkim=pass header.d=corp.com header.s=s1; spf=pass\r\n\r\n", "ceo@corp.com", true},
		{"dkim pass parent domain", "Authentication-Results: mx.net; dkim=pass header.d=corp.com\r\n\r\n", "ceo@mail.corp.com", true},
		{"dkim pass via header.i", "Authentication-Results: mx.net; dkim=pass header.i=@corp.com\r\n\r\n", "ceo@corp.com", true},
		{"dkim pass wrong domain", "Authentication-Results: mx.net; dkim=pass header.d=attacker.net\r\n\r\n", "ceo@corp.com", false},
		{"dkim fail", "Authentication-Results: mx.net; dkim=fail header.d=corp.com\r\n\r\n", "ceo@corp.com", false},
		{"spf only", "Authentication-Results: mx.net; spf=pass smtp.mailfrom=corp.com\r\n\r\n", "ceo@corp.com", false},
		{"no header", "Subject: hi\r\n\r\n", "ceo@corp.com", false},
		{"folded header", "Authentication-Results: mx.net;\r\n\tdkim=pass header.d=corp.com\r\n\r\n", "ceo@corp.com", true},
		{"other ids ignored, ours decides", "Authentication-Results: other; dkim=fail header.d=corp.com\r\nAuthentication-Results: mx.net; dkim=pass header.d=corp.com\r\n\r\n", "ceo@corp.com", true},
		{"from without domain", "Authentication-Results: mx.net; dkim=pass header.d=corp.com\r\n\r\n", "ceo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, authResultsPass(headerMsg(tc.hdr), tc.from, "mx.net"))
		})
	}
	assert.False(t, authResultsPass(nil, "a@b.c", "mx.net"))
}

// The header section fetched for the DKIM gate must never be mistaken
// for the body.
func TestExtractBody_SkipsHeaderSection(t *testing.T) {
	m := &imapclient.FetchMessageBuffer{BodySection: []imapclient.FetchBodySectionBuffer{
		{Section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader}, Bytes: []byte("Authentication-Results: x\r\n\r\n")},
		{Section: &imap.FetchItemBodySection{}, Bytes: []byte("Subject: x\r\n\r\nhello")},
	}}
	assert.Equal(t, "hello", extractBody(m))
}

// A sender can write their own Authentication-Results header; the MTA
// only strips headers carrying its own authserv-id. A forged header
// with another id must never pass, wherever it sits.
func TestAuthResults_ForgedHeaderIsIgnored(t *testing.T) {
	cases := map[string]string{
		"forged below our real fail": "Authentication-Results: mx.net; dkim=fail header.d=corp.com\r\n" +
			"Authentication-Results: evil.example; dkim=pass header.d=corp.com\r\n\r\n",
		"forged above our real fail": "Authentication-Results: evil.example; dkim=pass header.d=corp.com\r\n" +
			"Authentication-Results: mx.net; dkim=fail header.d=corp.com\r\n\r\n",
		"only a forged header":         "Authentication-Results: evil.example; dkim=pass header.d=corp.com; dmarc=pass header.from=corp.com\r\n\r\n",
		"our id inside a forged value": "Authentication-Results: evil.example mx.net; dkim=pass header.d=corp.com\r\n\r\n",
	}
	for name, hdr := range cases {
		t.Run(name, func(t *testing.T) {
			assert.False(t, authResultsPass(headerMsg(hdr), "ceo@corp.com", "mx.net"))
		})
	}
}

// When our id appears twice, the topmost header (the last one our MTA
// added on receipt) decides; a lower copy may be a forgery the MTA
// failed to strip.
func TestAuthResults_TopmostTrustedHeaderDecides(t *testing.T) {
	hdr := "Authentication-Results: mx.net; dkim=fail header.d=corp.com\r\n" +
		"Authentication-Results: mx.net; dkim=pass header.d=corp.com\r\n\r\n"
	assert.False(t, authResultsPass(headerMsg(hdr), "ceo@corp.com", "mx.net"))
}

func TestAuthResults_DMARCDecidesWhenPresent(t *testing.T) {
	pass := "Authentication-Results: mx.net; dkim=pass header.d=corp.com; dmarc=pass header.from=corp.com\r\n\r\n"
	fail := "Authentication-Results: mx.net; dkim=pass header.d=corp.com; dmarc=fail header.from=corp.com\r\n\r\n"
	other := "Authentication-Results: mx.net; dkim=pass header.d=corp.com; dmarc=fail header.from=else.com\r\n\r\n"
	assert.True(t, authResultsPass(headerMsg(pass), "ceo@corp.com", "mx.net"))
	assert.False(t, authResultsPass(headerMsg(fail), "ceo@corp.com", "mx.net"), "dmarc=fail overrides an aligned dkim=pass")
	assert.True(t, authResultsPass(headerMsg(other), "ceo@corp.com", "mx.net"), "a DMARC result for another domain does not apply")
}

func TestAuthResults_VersionedAuthservIDAndCase(t *testing.T) {
	hdr := "Authentication-Results: MX.NET 1; dkim=pass header.d=corp.com\r\n\r\n"
	assert.True(t, authResultsPass(headerMsg(hdr), "ceo@corp.com", "mx.net"))
}

func TestAuthResults_EmptyTrustedIDNeverPasses(t *testing.T) {
	hdr := "Authentication-Results: mx.net; dkim=pass header.d=corp.com\r\n\r\n"
	assert.False(t, authResultsPass(headerMsg(hdr), "ceo@corp.com", " "))
}

func FuzzAuthResults(f *testing.F) {
	f.Add("Authentication-Results: mx.net; dkim=pass header.d=corp.com\r\n\r\n")
	f.Add("Authentication-Results: evil; dkim=pass header.d=corp.com\r\n\r\n")
	f.Add("Authentication-Results: mx.net;\r\n\tdmarc=pass header.from=corp.com\r\n\r\n")
	f.Fuzz(func(t *testing.T, raw string) {
		got := authResultsPass(headerMsg(raw), "ceo@corp.com", "mx.net")
		if got && !strings.Contains(strings.ToLower(raw), "mx.net") {
			t.Fatalf("passed without our authserv-id: %q", raw)
		}
	})
}
