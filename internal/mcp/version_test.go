package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsLegacyProtocolVersion(t *testing.T) {
	for _, v := range LegacyProtocolVersions {
		assert.True(t, IsLegacyProtocolVersion(v), v)
	}
	assert.Equal(t, LatestLegacyProtocolVersion, LegacyProtocolVersions[0], "newest first")
	assert.False(t, IsLegacyProtocolVersion(ModernProtocolVersion), "2026-07-28 has no initialize handshake")
	assert.False(t, IsLegacyProtocolVersion("2023-01-01"))
}
