package email

import "testing"

// FuzzStripHeaders drives arbitrary bytes through the RFC 5322 header
// separator. The pathological cases are the CRLF variants (\r\n\r\n,
// \n\n, missing-body, tail-only) plus binary content that could
// accidentally form a header separator inside a base64 attachment.
func FuzzStripHeaders(f *testing.F) {
	f.Add("Subject: hi\r\n\r\nBody")
	f.Add("Subject: hi\n\nBody")
	f.Add("Body-only-no-headers")
	f.Add("")
	f.Add("Subject: hi")
	f.Add("Subject: hi\r\n\r\n")
	f.Add("A\r\n\r\nB\r\n\r\nC")

	f.Fuzz(func(t *testing.T, raw string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %q: %v", raw, r)
			}
		}()
		_ = stripHeaders(raw)
	})
}

// FuzzBuildMessage checks that RFC 5322 rendering never panics on
// arbitrary from / to / body — including embedded CRLFs which are a
// classic SMTP injection vector.
func FuzzBuildMessage(f *testing.F) {
	f.Add("bot@x", "user@y", "hello")
	f.Add("", "", "")
	f.Add("bot@x\r\nSubject: injected", "user@y", "body")
	f.Add("bot@x", "user@y\r\nCc: attacker@z", "body")
	f.Add("bot@x", "user@y", "line1\r\nline2\r\n.\r\n")

	f.Fuzz(func(t *testing.T, from, to, body string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on from=%q to=%q body=%q: %v", from, to, body, r)
			}
		}()
		_ = buildMessage(from, to, body)
	})
}
