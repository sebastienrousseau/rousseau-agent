package agent_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools"
)

// stubProvider is the smallest thing satisfying agent.Provider.
type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }

func (stubProvider) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{}, errors.New("stub provider does not complete")
}

// TestNew_BuildsUsableAgent exercises the facade's New alias — the one
// public constructor the pkg_test shape check does not reach.
func TestNew_BuildsUsableAgent(t *testing.T) {
	reg := tools.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	a := agent.New(stubProvider{}, reg, logger, agent.Options{MaxIterations: 1})
	require.NotNil(t, a)

	sess := agent.NewSession("facade")
	sess.Messages = append(sess.Messages, agent.NewUserText("hello"))
	_, err := a.Turn(context.Background(), sess)
	assert.Error(t, err, "the stub provider always fails, proving the agent was wired to it")
}
