package bedrock_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	pkgagent "github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/bedrock"
)

var _ pkgagent.Provider = (*bedrock.Provider)(nil)

func TestNew_RejectsIncompleteConfig(t *testing.T) {
	_, err := bedrock.New(context.Background(), bedrock.Config{})
	assert.ErrorContains(t, err, "Region is required")
	_, err = bedrock.New(context.Background(), bedrock.Config{Region: "eu-west-1"})
	assert.ErrorContains(t, err, "Model is required")
}
