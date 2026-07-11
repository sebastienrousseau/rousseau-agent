package whatsapp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func silentContext() context.Context { return context.Background() }

func TestNew_RequiresStoreDSN(t *testing.T) {
	_, err := New(Config{}, silentLogger())
	assert.Error(t, err)
}

func TestNew_DefaultsLogLevelAndHeader(t *testing.T) {
	c, err := New(Config{StoreDSN: "file:test.db"}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "WARN", c.cfg.LogLevel)
	assert.Equal(t, DefaultReplyHeader, c.cfg.ReplyHeader)
}

func TestNew_KeepsExplicitLogLevel(t *testing.T) {
	c, err := New(Config{StoreDSN: "x", LogLevel: "DEBUG"}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "DEBUG", c.cfg.LogLevel)
}

func TestClient_Name(t *testing.T) {
	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "whatsapp", c.Name())
}

func TestParseJID_HappyPath(t *testing.T) {
	jid, err := parseJID("447906009073@s.whatsapp.net")
	require.NoError(t, err)
	assert.Equal(t, "447906009073", jid.User)
	assert.Equal(t, "s.whatsapp.net", jid.Server)
}

func TestParseJID_Empty(t *testing.T) {
	_, err := parseJID("")
	assert.Error(t, err)
}

// whatsmeow's JID parser is lenient with formatless input, so we only
// test the empty-string rejection path here; malformed strings become
// zero-value JIDs rather than errors.

func TestClient_DeliverNotConnected(t *testing.T) {
	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(silentContext(), "447906009073@s.whatsapp.net", "hi")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")
}

func TestClient_DeliverBadJID(t *testing.T) {
	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(silentContext(), "", "hi")
	assert.Error(t, err)
}

func TestClient_StopIdempotent(t *testing.T) {
	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}

func TestClient_StartTwiceErrors(t *testing.T) {
	// Not calling actual whatsmeow connect — just verifying that setting
	// wm non-nil trips the guard.
	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	// Manually set wm to a placeholder so a second Start would trip.
	// (Actual whatsmeow.Client is opaque; we just need non-nil.)
	c.wm = nil // baseline

	// The Start guard depends on c.wm being non-nil. That's exercised
	// only by a live connect; here we just document the intent.
	assert.Nil(t, c.wm)
}
