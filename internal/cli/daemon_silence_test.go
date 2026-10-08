package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Through the daemon's real handler chain, a sender outside the
// allowlist gets no answer to a control verb or a chat command.
func TestTransportHandler_StrangerGetsNoReply(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	wiring, err := assembleDaemon(context.Background(), opts, []string{"+owner"})
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup

	h := wiring.TransportHandler("whatsapp", silentLogger())
	for _, body := range []string{"/status", "/cancel", "/version", "/help"} {
		reply, err := h.Handle(context.Background(), transport.IncomingMessage{From: "+stranger", Body: body})
		require.NoError(t, err)
		assert.Empty(t, reply, "%s from a stranger must get no reply", body)
	}
	reply, err := h.Handle(context.Background(), transport.IncomingMessage{From: "+owner", Body: "/version"})
	require.NoError(t, err)
	assert.NotEmpty(t, reply, "the allowlisted owner still gets answers")
}
