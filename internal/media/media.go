// Package media handles the size / mime-sniff / decoding rules that
// every image-capable transport applies before pushing a
// [agent.ContentImage] block into a session.
//
// The policy is deliberately conservative: the transport envelope's
// self-reported media type is not trusted (attackers can lie), MIME
// is sniffed from the first 512 bytes, oversize images are dropped
// with a metric increment rather than truncated, and a per-turn
// total-size cap prevents a single message from blowing the token
// budget.
package media

import (
	"errors"
	"fmt"
	"net/http"
)

// DefaultMaxImageBytes is the per-image ceiling. 10 MiB matches the
// Anthropic API limit at time of writing.
const DefaultMaxImageBytes = 10 * 1024 * 1024

// DefaultMaxTotalBytes is the per-turn attachment total. 40 MiB
// permits four maxed-out images per turn.
const DefaultMaxTotalBytes = 40 * 1024 * 1024

// DefaultAllowedMIMEs is the shipped MIME allowlist. Every current
// direct provider supports this exact set; adding another entry
// requires verifying provider support.
var DefaultAllowedMIMEs = []string{
	"image/png",
	"image/jpeg",
	"image/webp",
	"image/gif",
}

// Policy encapsulates the media-acceptance rules. Zero-value Policy
// uses the Default* values above.
type Policy struct {
	MaxImageBytes int
	MaxTotalBytes int
	AllowedMIMEs  []string
}

func (p Policy) maxImage() int {
	if p.MaxImageBytes > 0 {
		return p.MaxImageBytes
	}
	return DefaultMaxImageBytes
}

func (p Policy) maxTotal() int {
	if p.MaxTotalBytes > 0 {
		return p.MaxTotalBytes
	}
	return DefaultMaxTotalBytes
}

func (p Policy) allowed() []string {
	if len(p.AllowedMIMEs) > 0 {
		return p.AllowedMIMEs
	}
	return DefaultAllowedMIMEs
}

// Errors returned by [Policy.Accept].
var (
	ErrTooLarge       = errors.New("media: image exceeds per-image size limit")
	ErrTotalTooLarge  = errors.New("media: attachments exceed per-turn total")
	ErrDisallowedMIME = errors.New("media: sniffed MIME type not in allowlist")
)

// Accept verifies a candidate image against the policy. Returns the
// canonical media type (sniffed from data) on success. On failure
// the returned error wraps one of the Err* sentinels above so callers
// can distinguish user-facing "sorry, too big" from operator-facing
// "your allowlist rejected this."
//
// totalSoFar counts the already-accepted attachment bytes in the
// current turn — pass 0 on the first attachment.
func (p Policy) Accept(data []byte, totalSoFar int) (mime string, err error) {
	if len(data) > p.maxImage() {
		return "", fmt.Errorf("%w: %d bytes > %d limit", ErrTooLarge, len(data), p.maxImage())
	}
	if totalSoFar+len(data) > p.maxTotal() {
		return "", fmt.Errorf("%w: %d + %d bytes > %d limit",
			ErrTotalTooLarge, totalSoFar, len(data), p.maxTotal())
	}
	sniffLen := 512
	if len(data) < sniffLen {
		sniffLen = len(data)
	}
	sniffed := http.DetectContentType(data[:sniffLen])
	// http.DetectContentType returns e.g. "image/png; charset=..."
	// occasionally; trim any parameter suffix.
	base := stripParams(sniffed)
	for _, m := range p.allowed() {
		if base == m {
			return base, nil
		}
	}
	return "", fmt.Errorf("%w: sniffed %q, allowed %v", ErrDisallowedMIME, base, p.allowed())
}

func stripParams(mime string) string {
	for i, r := range mime {
		if r == ';' || r == ' ' {
			return mime[:i]
		}
	}
	return mime
}
