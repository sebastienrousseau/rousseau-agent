package email

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// rawMessage builds a fetched message whose full BODY[] section is raw.
func rawMessage(from, raw string) *imapclient.FetchMessageBuffer {
	m := mkMessage(from, "hi", "")
	m.BodySection = []imapclient.FetchBodySectionBuffer{{
		Section: &imap.FetchItemBodySection{},
		Bytes:   []byte(raw),
	}}
	return m
}

// pollRaw runs one poll over a single raw message and reports whether
// the handler ran, the body it saw, and what was sent.
func pollRaw(t *testing.T, from, raw string) (handled bool, body string, sent []string) {
	t.Helper()
	fake := &fakeIMAP{seqNums: []uint32{1}, messages: []*imapclient.FetchMessageBuffer{rawMessage(from, raw)}}
	c := mkClient(t, fake, nil, &sent)
	err := c.pollOnce(context.Background(), transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		handled = true
		body = m.Body
		return "reply", nil
	}))
	require.NoError(t, err)
	assert.True(t, fake.seenAdded, "dropped mail is still marked seen")
	return handled, body, sent
}

func TestPollOnce_DropsAutoReplies(t *testing.T) {
	cases := map[string]struct{ from, headers string }{
		"auto-submitted":    {"alice@example.com", "Auto-Submitted: auto-replied\r\n"},
		"auto-submitted-ci": {"alice@example.com", "Auto-Submitted: Auto-Generated; foo=bar\r\n"},
		"precedence-bulk":   {"alice@example.com", "Precedence: bulk\r\n"},
		"precedence-auto":   {"alice@example.com", "Precedence: auto_reply\r\n"},
		"list-id":           {"alice@example.com", "List-Id: <dev.lists.example.com>\r\n"},
		"list-unsubscribe":  {"alice@example.com", "List-Unsubscribe: <mailto:u@example.com>\r\n"},
		"mailer-daemon":     {"MAILER-DAEMON@mx.example.com", ""},
		"postmaster":        {"postmaster@example.com", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := "Subject: Out of office\r\n" + tc.headers + "\r\nI am away.\r\n"
			handled, _, sent := pollRaw(t, tc.from, raw)
			assert.False(t, handled, "handler must not run for auto mail")
			assert.Empty(t, sent, "nothing may be sent for auto mail")
		})
	}
}

func TestPollOnce_AutoSubmittedNoIsHandled(t *testing.T) {
	handled, body, sent := pollRaw(t, "alice@example.com",
		"Subject: hi\r\nAuto-Submitted: No\r\nPrecedence: first-class\r\n\r\nhello\r\n")
	assert.True(t, handled)
	assert.Equal(t, "hello", body)
	assert.Len(t, sent, 1)
}

func TestPollOnce_ReplyCarriesAutoSubmitted(t *testing.T) {
	_, _, sent := pollRaw(t, "alice@example.com", "Subject: hi\r\n\r\nhello\r\n")
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0], "\r\nAuto-Submitted: auto-replied\r\n")
}

func TestExtractBody_MultipartAlternativeKeepsTextPlain(t *testing.T) {
	raw := "Subject: hi\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"BND\"\r\n\r\n" +
		"preamble\r\n--BND\r\nContent-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\nplain caf=C3=A9 =\r\nbody\r\n" +
		"--BND\r\nContent-Type: text/html\r\n\r\n<p>html body</p>\r\n--BND--\r\n"
	got := extractBody(rawMessage("alice@example.com", raw))
	assert.Equal(t, "plain café body", got)
}

func TestExtractBody_Base64TextPart(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte("decoded text"))
	raw := "Subject: hi\r\nContent-Type: multipart/mixed; boundary=X\r\n\r\n" +
		"--X\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\n" + enc + "\r\n" +
		"--X\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\nAAAA\r\n--X--\r\n"
	assert.Equal(t, "decoded text", extractBody(rawMessage("alice@example.com", raw)))
}

func TestExtractBody_NoTextPlainIsEmpty(t *testing.T) {
	raw := "Subject: hi\r\nContent-Type: text/html\r\n\r\n<p>only html</p>\r\n"
	assert.Empty(t, extractBody(rawMessage("alice@example.com", raw)))
}

func TestExtractBody_CapsLargeBody(t *testing.T) {
	raw := "Subject: hi\r\n\r\n" + strings.Repeat("a", 1<<20)
	got := extractBody(rawMessage("alice@example.com", raw))
	assert.LessOrEqual(t, len(got), 256<<10+64, "body is capped near 256 KiB")
	assert.True(t, strings.HasSuffix(got, "[truncated]"), "capped body ends with the marker")
}
