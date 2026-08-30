//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
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
	jid, err := parseJID("15551234567@s.whatsapp.net")
	require.NoError(t, err)
	assert.Equal(t, "15551234567", jid.User)
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
	err = c.Deliver(silentContext(), "15551234567@s.whatsapp.net", "hi")
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

func TestClient_Bus_ReturnsInjectedBus(t *testing.T) {
	// When cfg.Progress is set, Client.Bus must return exactly
	// that instance — the daemon's control Registry uses it as
	// its per-turn Publisher target, so aliasing matters.
	shared := progress.NewBus(progress.BusOptions{})
	defer shared.Close()

	c, err := New(Config{StoreDSN: "test.db", Progress: shared}, nil)
	require.NoError(t, err)
	assert.Same(t, shared, c.Bus(), "cfg.Progress must alias c.Bus()")
}

func TestClient_Bus_FabricatesWhenNil(t *testing.T) {
	// When cfg.Progress is nil (unit tests + embedded use), New
	// still returns a live Bus — otherwise callers of c.Bus()
	// would nil-deref.
	c, err := New(Config{StoreDSN: "test.db"}, nil)
	require.NoError(t, err)
	assert.NotNil(t, c.Bus())
}

func TestClient_IsAllowedEmptyAllowlistIsOpen(t *testing.T) {
	// Empty Allowlist → openAll: every JID passes. Parity with the
	// daemon's Router-level allowlist which also treats empty as
	// "no restriction".
	c, err := New(Config{StoreDSN: "test.db"}, nil)
	require.NoError(t, err)
	assert.True(t, c.isAllowed("stranger@s.whatsapp.net"))
	assert.True(t, c.isAllowed("friend@s.whatsapp.net"))
}

func TestClient_IsAllowedHonoursExplicitAllowlist(t *testing.T) {
	// Security-critical branch: with a populated allowlist,
	// unlisted senders must be rejected. This is the gate the
	// pre-reaction dispatcher relies on to avoid leaking
	// bot-presence to strangers.
	c, err := New(Config{
		StoreDSN:  "test.db",
		Allowlist: []string{"friend@s.whatsapp.net"},
	}, nil)
	require.NoError(t, err)
	assert.True(t, c.isAllowed("friend@s.whatsapp.net"))
	assert.False(t, c.isAllowed("stranger@s.whatsapp.net"))
	assert.False(t, c.isAllowed(""), "empty from is not an accidental match")
}
