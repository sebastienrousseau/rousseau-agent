package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestSignalCmd_MissingAccountErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newSignalCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "account")
}

func TestSignalCmd_HasFlags(t *testing.T) {
	cmd := newSignalCmd(&Options{Config: &config.Config{}})
	assert.NotNil(t, cmd.Flags().Lookup("account"))
	assert.NotNil(t, cmd.Flags().Lookup("binary"))
	assert.NotNil(t, cmd.Flags().Lookup("allow"))
}
