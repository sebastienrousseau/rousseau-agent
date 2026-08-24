package claudecli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgcli "github.com/sebastienrousseau/rousseau-agent/pkg/llm/claudecli"
)

func TestNew_DefaultBinary(t *testing.T) {
	// Empty Config still returns a functional Provider — the
	// underlying constructor substitutes "claude" for Binary.
	p := pkgcli.New(pkgcli.Config{})
	require.NotNil(t, p)
	assert.Equal(t, "claudecli", p.Name())
}

func TestNew_ExplicitConfig(t *testing.T) {
	cfg := pkgcli.Config{
		Binary:         "/opt/claude/bin/claude",
		Model:          "claude-sonnet-4-6",
		PermissionMode: "bypassPermissions",
		ExtraArgs:      []string{"--bare"},
	}
	p := pkgcli.New(cfg)
	require.NotNil(t, p)
	assert.Equal(t, "claudecli", p.Name())
}

func TestConfig_TypeAlias_Assignable(t *testing.T) {
	// Composite-literal Config via pkg alias — verifies alias is
	// transparent so external callers can construct Config values
	// directly.
	var cfg pkgcli.Config = pkgcli.Config{Binary: "x", Model: "y"}
	assert.Equal(t, "x", cfg.Binary)
	assert.Equal(t, "y", cfg.Model)
}
