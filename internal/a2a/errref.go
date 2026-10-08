package a2a

import (
	"crypto/rand"
	"encoding/hex"
)

// ErrorRef returns a short random reference that ties a generic,
// peer-facing error message to the log line carrying its detail.
// Peers see only "<summary> (ref <ErrorRef>)"; operators grep the
// logs for the reference. The reference carries no information about
// the error itself.
func ErrorRef() string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read does not fail on supported platforms
	return hex.EncodeToString(b[:])
}

// PeerErrorMessage formats a generic peer-facing message with its
// reference: "task failed (ref 0a1b2c3d4e5f)".
func PeerErrorMessage(summary, ref string) string {
	return summary + " (ref " + ref + ")"
}
