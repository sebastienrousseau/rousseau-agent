package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// End to end through the provider the daemon wires: Bob's recall must
// never contain Alice's history, whatever words Bob uses.
func TestRecall_NeverCrossesSenders(t *testing.T) {
	ctx := context.Background()
	store, err := openSearchableStore(ctx, config.StateConfig{Path: t.TempDir() + "/s.db"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

	alice := agent.NewSession("chat")
	alice.Sender = "telegram:alice"
	alice.Append(agent.NewUserText("my salary is 91000 and the invoice password is hunter2"))
	require.NoError(t, store.Save(ctx, alice))

	bobOld := agent.NewSession("chat")
	bobOld.Sender = "telegram:bob"
	bobOld.Append(agent.NewUserText("bob asked about the invoice template"))
	require.NoError(t, store.Save(ctx, bobOld))

	bob := agent.NewSession("chat")
	bob.Sender = "telegram:bob"
	bob.Append(agent.NewUserText("what was the invoice password and salary"))

	got := buildRecallProvider(store).SystemAppendix(ctx, bob)
	assert.NotContains(t, got, "hunter2")
	assert.NotContains(t, got, "91000")
	assert.Contains(t, got, "invoice template", "Bob still recalls his own history")
}
