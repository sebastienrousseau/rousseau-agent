package composio

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEnvAPIKeyName pins the env-var names so a config-doc rename
// doesn't silently break operator setups.
func TestEnvAPIKeyName(t *testing.T) {
	assert.Equal(t, "ROUSSEAU_COMPOSIO_API_KEY", EnvAPIKey)
	assert.Equal(t, "ROUSSEAU_COMPOSIO_USER_ID", EnvUserID)
}

// TestEnvironmentIsolated confirms that clearing the env vars really
// clears them so other tests don't leak.
func TestEnvironmentIsolated(t *testing.T) {
	orig := os.Getenv(EnvAPIKey)
	t.Setenv(EnvAPIKey, "")
	defer func() { _ = os.Setenv(EnvAPIKey, orig) }() //nolint:errcheck // test cleanup
	assert.Empty(t, os.Getenv(EnvAPIKey))
}
