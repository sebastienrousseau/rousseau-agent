package plan_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/plan"
)

func TestNew_ReturnsScaffold(t *testing.T) {
	_, err := plan.New()
	assert.ErrorIs(t, err, plan.ErrScaffold)
}
