package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestAllowGroupsByTransport(t *testing.T) {
	cfg := &config.Config{}
	got := allowGroupsByTransport(cfg)
	for name, on := range got {
		assert.False(t, on, "%s answers groups only when allow_groups is set", name)
	}
	cfg.Telegram.AllowGroups = true
	cfg.Matrix.AllowGroups = true
	got = allowGroupsByTransport(cfg)
	assert.True(t, got["telegram"])
	assert.True(t, got["matrix"])
	assert.False(t, got["slack"])
	assert.False(t, got["whatsapp"], "whatsapp skips groups in the transport")
}

func TestBuildSSO_RequiresAudience(t *testing.T) {
	_, _, err := buildSSO(t.Context(), config.SSOConfig{
		Kind: "oidc", OIDC: config.SSOOIDCConfig{Issuer: "https://idp.example"},
	}, nil, nil, discardLogger())
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "sso.oidc.audience is required")
	}
}
