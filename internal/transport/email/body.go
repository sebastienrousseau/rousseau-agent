package email

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"

	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	// maxBodyBytes caps the text handed to the handler, so a huge mail
	// cannot feed an unbounded prompt.
	maxBodyBytes = 256 << 10
	// truncatedMarker is appended to a body cut at maxBodyBytes.
	truncatedMarker = "\n[truncated]"
	// maxMIMEDepth bounds multipart nesting.
	maxMIMEDepth = 5
)

// fullSection returns the raw RFC 5322 message (the BODY[] section),
// or nil when the fetch carried none.
func fullSection(m *imapclient.FetchMessageBuffer) []byte {
	if m == nil {
		return nil
	}
	for _, section := range m.BodySection {
		if len(section.Bytes) > 0 && !isHeaderSection(section.Section) {
			return section.Bytes
		}
	}
	return nil
}

// readFull parses the fetched message and returns its header (nil when
// it cannot be parsed) and its plain-text body, decoded and capped.
// Unparseable mail falls back to the text after the header block.
func readFull(m *imapclient.FetchMessageBuffer) (mail.Header, string) {
	raw := fullSection(m)
	if raw == nil {
		return nil, ""
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, capBody(strings.NewReader(stripHeaders(string(raw))))
	}
	part := textPart(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0)
	if part == nil {
		return msg.Header, ""
	}
	return msg.Header, capBody(part)
}

// extractBody returns the plain-text body of a fetched message: the
// first text/plain MIME part, transfer-decoded and capped at
// maxBodyBytes. Mail with no text/plain part yields "".
func extractBody(m *imapclient.FetchMessageBuffer) string {
	_, body := readFull(m)
	return body
}

// textPart returns a decoded reader over the first text/plain entity in
// body, or nil when there is none. A missing Content-Type is text/plain
// (RFC 2045 §5.2).
func textPart(contentType, encoding string, body io.Reader, depth int) io.Reader {
	mediaType, params := "text/plain", map[string]string(nil)
	if contentType != "" {
		mt, p, err := mime.ParseMediaType(contentType)
		if mt == "" && err != nil {
			return nil
		}
		mediaType, params = mt, p
	}
	switch {
	case mediaType == "text/plain":
		return decodeTransfer(encoding, body)
	case strings.HasPrefix(mediaType, "multipart/") && depth < maxMIMEDepth:
		return firstTextPart(multipart.NewReader(body, params["boundary"]), depth)
	}
	return nil
}

// firstTextPart walks a multipart body and returns the first text/plain
// part found, descending into nested multiparts.
func firstTextPart(r *multipart.Reader, depth int) io.Reader {
	for {
		p, err := r.NextRawPart()
		if err != nil {
			return nil // io.EOF or a malformed body: no text part
		}
		h := p.Header
		if out := textPart(h.Get("Content-Type"), h.Get("Content-Transfer-Encoding"), p, depth+1); out != nil {
			return out
		}
	}
}

// decodeTransfer undoes a quoted-printable or base64 transfer encoding;
// 7bit, 8bit, binary and unknown encodings pass through. The base64
// decoder already skips the CR/LF line breaks of a MIME body.
func decodeTransfer(encoding string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	}
	return r
}

// capBody reads at most maxBodyBytes of r, trims it, and marks a cut
// body. A decode error keeps what was read before it.
func capBody(r io.Reader) string {
	buf, err := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil && len(buf) == 0 {
		return ""
	}
	if len(buf) <= maxBodyBytes {
		return strings.TrimSpace(string(buf))
	}
	// Dropping invalid UTF-8 removes a multi-byte rune split by the cut.
	cut := strings.ToValidUTF8(string(buf[:maxBodyBytes]), "")
	return strings.TrimSpace(cut) + truncatedMarker
}
