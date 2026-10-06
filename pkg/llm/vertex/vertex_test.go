package vertex_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	pkgagent "github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/vertex"
)

var _ pkgagent.Provider = (*vertex.Provider)(nil)

func TestNew_RejectsIncompleteConfig(t *testing.T) {
	_, err := vertex.New(context.Background(), vertex.Config{})
	assert.ErrorContains(t, err, "Project is required")
	_, err = vertex.New(context.Background(), vertex.Config{Project: "p", Region: "europe-west1"})
	assert.ErrorContains(t, err, "Model is required")
}
