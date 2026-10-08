package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/model"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// failingProvider fails every completion with internal-looking detail.
type failingProvider struct{}

func (failingProvider) Name() string { return "failing" }

func (failingProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("upstream 500 from 10.9.8.7: internal-secret-detail")
}

// TestOnTask_TurnErrorNotLeaked: a failed turn reaches the peer as a
// generic turn_error with a reference; the detail stays in the log.
func TestOnTask_TurnErrorNotLeaked(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	ag := agent.New(failingProvider{}, tools.NewRegistry(), logger, agent.Options{})
	h := newA2ATaskHandler(ag, nil, logger)

	var last a2a.TaskUpdate
	require.NoError(t, h.OnTask(context.Background(), a2a.Task{TaskID: "t1", Prompt: "hi"}, func(u a2a.TaskUpdate) { last = u }))

	assert.Equal(t, a2a.TaskStatusFailed, last.Status)
	assert.Equal(t, "turn_error", last.FailureCode)
	assert.NotContains(t, last.Message, "internal-secret-detail")
	assert.Contains(t, last.Message, "ref ")
	assert.Contains(t, logs.String(), "internal-secret-detail")
}
