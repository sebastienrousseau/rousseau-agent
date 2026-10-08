package transport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLinkCodes_Expire(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewLinkCodes()
	c.now = func() time.Time { return now }
	code := c.Issue("id-1", "slack", "U1")
	now = now.Add(11 * time.Minute)
	_, ok := c.Redeem("slack", "U1", code)
	assert.False(t, ok, "a code is valid for 10 minutes")
}

func TestLinkCodes_RedeemOnce(t *testing.T) {
	c := NewLinkCodes()
	code := c.Issue("id-1", "slack", "U1")
	id, ok := c.Redeem("slack", "U1", code)
	assert.True(t, ok)
	assert.Equal(t, "id-1", id)
	_, ok = c.Redeem("slack", "U1", code)
	assert.False(t, ok)
	assert.Len(t, code, 6)
}
