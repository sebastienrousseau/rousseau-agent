package progress

import (
	"crypto/rand"
	"encoding/hex"
)

// NewRef returns a short random reference id ("7f3a2c") that ties a
// failure shown in chat to the log line carrying the full error.
// Publishers log the error once with the field "ref" and put
// [FailureText] of the same ref on the event.
func NewRef() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "000000" // unreachable since Go 1.24: Read crashes instead
	}
	return hex.EncodeToString(b[:])
}

// FailureText is the chat-safe stand-in for an error:
// "failed (ref 7f3a2c)".
func FailureText(ref string) string {
	return "failed (ref " + ref + ")"
}
