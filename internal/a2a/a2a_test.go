package a2a_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// The scaffold currently owns just the payload types. These tests
// pin their JSON shape so downstream refactors don't accidentally
// rename a field the wire protocol depends on.

func TestCapabilityCard_JSONShape(t *testing.T) {
	card := a2a.CapabilityCard{
		AgentID: "rousseau-1",
		Name:    "rousseau-agent",
		Version: "v0.0.1",
		Skills: []a2a.SkillDescriptor{
			{Name: "git-rebase", Description: "safe rebases"},
		},
		SupportsStreaming: true,
	}
	blob, err := json.Marshal(card)
	require.NoError(t, err)
	assert.Contains(t, string(blob), `"agent_id":"rousseau-1"`)
	assert.Contains(t, string(blob), `"supports_streaming":true`)
	assert.Contains(t, string(blob), `"skills":[`)
}

func TestTask_JSONShape(t *testing.T) {
	task := a2a.Task{
		TaskID:    "t-1",
		FromAgent: "peer",
		Prompt:    "review these changes",
	}
	blob, err := json.Marshal(task)
	require.NoError(t, err)
	assert.Contains(t, string(blob), `"task_id":"t-1"`)
	assert.Contains(t, string(blob), `"from_agent":"peer"`)
}

func TestTaskUpdate_StatusConstants(t *testing.T) {
	// The status enum values are the wire strings — pin them so we
	// don't accidentally send an unknown status to a peer.
	assert.Equal(t, a2a.TaskStatus("running"), a2a.TaskStatusRunning)
	assert.Equal(t, a2a.TaskStatus("completed"), a2a.TaskStatusCompleted)
	assert.Equal(t, a2a.TaskStatus("failed"), a2a.TaskStatusFailed)
	assert.Equal(t, a2a.TaskStatus("cancelled"), a2a.TaskStatusCancelled)
}

func TestArtifact_JSONShape(t *testing.T) {
	a := a2a.Artifact{
		URI:       "https://example.com/x.txt",
		MimeType:  "text/plain",
		Name:      "x.txt",
		SizeBytes: 100,
	}
	blob, err := json.Marshal(a)
	require.NoError(t, err)
	assert.Contains(t, string(blob), `"mime_type":"text/plain"`)
	assert.Contains(t, string(blob), `"size_bytes":100`)
}
