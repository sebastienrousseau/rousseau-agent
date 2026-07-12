package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestSlackCmd_MissingTokensErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newSlackCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token")
}

func TestSlackCmd_HasFlags(t *testing.T) {
	cmd := newSlackCmd(&Options{Config: &config.Config{}})
	assert.NotNil(t, cmd.Flags().Lookup("app-token"))
	assert.NotNil(t, cmd.Flags().Lookup("bot-token"))
	assert.NotNil(t, cmd.Flags().Lookup("bot-user-id"))
	assert.NotNil(t, cmd.Flags().Lookup("allow"))
}

func TestDiscordCmd_MissingTokenErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newDiscordCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token")
}

func TestDiscordCmd_HasFlags(t *testing.T) {
	cmd := newDiscordCmd(&Options{Config: &config.Config{}})
	assert.NotNil(t, cmd.Flags().Lookup("token"))
	assert.NotNil(t, cmd.Flags().Lookup("allow"))
}
