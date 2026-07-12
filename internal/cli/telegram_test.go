package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestTelegramCmd_MissingTokenErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newTelegramCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token")
}

func TestTelegramCmd_HasFlags(t *testing.T) {
	cmd := newTelegramCmd(&Options{Config: &config.Config{}})
	assert.NotNil(t, cmd.Flags().Lookup("token"))
	assert.NotNil(t, cmd.Flags().Lookup("allow"))
}
