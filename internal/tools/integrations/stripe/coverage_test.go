package stripe

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func TestEveryToolExposesMetadata(t *testing.T) {
	c, err := New(Config{SecretKey: "sk_test_x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	for _, name := range reg.Names() {
		tool, ok := reg.Get(name)
		require.True(t, ok)
		assert.NotEmpty(t, tool.Description())
		schema := tool.InputSchema()
		assert.Equal(t, "object", schema["type"])
	}
}

func TestListChargesTool_HandlesEmptyInput(t *testing.T) {
	c, err := New(Config{SecretKey: "sk_test_x"})
	require.NoError(t, err)
	tool := NewListChargesTool(c)
	_, err = tool.Execute(context.Background(), nil)
	if err != nil {
		assert.NotContains(t, err.Error(), "bad input")
	}
}

func TestListChargesTool_CustomerFilter(t *testing.T) {
	c, err := New(Config{SecretKey: "sk_test_x"})
	require.NoError(t, err)
	tool := NewListChargesTool(c)
	// The failure is on the wire; the input parse must not fail.
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"customer":"cus_123","limit":5}`))
	if err != nil {
		assert.NotContains(t, err.Error(), "bad input")
	}
}
