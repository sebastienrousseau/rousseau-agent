package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestMatrixCmd_MissingHomeserverErrors(t *testing.T) {
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	cmd := newMatrixCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "homeserver")
}

func TestMatrixCmd_MissingTokenErrors(t *testing.T) {
	opts := &Options{
		Config: &config.Config{Matrix: config.MatrixConfig{HomeserverURL: "http://x"}},
		Logger: silentLogger(),
	}
	cmd := newMatrixCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	assert.Error(t, err)
}

func TestMatrixCmd_HasFlags(t *testing.T) {
	cmd := newMatrixCmd(&Options{Config: &config.Config{}})
	assert.NotNil(t, cmd.Flags().Lookup("homeserver"))
	assert.NotNil(t, cmd.Flags().Lookup("token"))
	assert.NotNil(t, cmd.Flags().Lookup("user-id"))
	assert.NotNil(t, cmd.Flags().Lookup("allow"))
}
