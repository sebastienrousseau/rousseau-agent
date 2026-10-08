package transport

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

// linkCodeTTL is how long a /link confirmation code stays valid.
const linkCodeTTL = 10 * time.Minute

// LinkCodes holds the pending /link requests: a six-digit code the
// target handle must send back with /confirm before it is linked, so
// nobody can attach a handle they do not control to their identity.
// One pending request per target handle; a wrong code cancels it.
// Share one LinkCodes between the routers of every transport.
type LinkCodes struct {
	mu      sync.Mutex
	pending map[string]linkRequest
	now     func() time.Time
}

type linkRequest struct {
	identity string
	code     string
	expires  time.Time
}

// NewLinkCodes returns an empty set of pending link requests.
func NewLinkCodes() *LinkCodes {
	return &LinkCodes{pending: map[string]linkRequest{}, now: time.Now}
}

// Issue records that identity asked to link (tp, sender) and returns
// the code that handle must send. A new request replaces an older one.
func (c *LinkCodes) Issue(identity, tp, sender string) string {
	var b [4]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	// The modulo bias over 2^32 is below one in four thousand.
	code := fmt.Sprintf("%06d", binary.BigEndian.Uint32(b[:])%1_000_000)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[tp+":"+sender] = linkRequest{identity: identity, code: code, expires: c.now().Add(linkCodeTTL)}
	return code
}

// Redeem returns the identity that asked to link (tp, sender) when code
// matches its unexpired request. The request is consumed either way.
func (c *LinkCodes) Redeem(tp, sender, code string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := tp + ":" + sender
	req, ok := c.pending[key]
	delete(c.pending, key)
	if !ok || c.now().After(req.expires) {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(req.code), []byte(code)) != 1 {
		return "", false
	}
	return req.identity, true
}
