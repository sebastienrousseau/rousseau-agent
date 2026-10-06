package email

import (
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
		{"second header wins", "Authentication-Results: other; dkim=fail header.d=corp.com\r\nAuthentication-Results: mx.net; dkim=pass header.d=corp.com\r\n\r\n", "ceo@corp.com", true},
		{"from without domain", "Authentication-Results: mx.net; dkim=pass header.d=corp.com\r\n\r\n", "ceo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, authResultsPass(headerMsg(tc.hdr), tc.from))
		})
	}
	assert.False(t, authResultsPass(nil, "a@b.c"))
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
