package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToSDKInputSchema_ForwardsRequiredAndExtraKeywords(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
		"$defs":                map[string]any{"x": map[string]any{"type": "integer"}},
	}
	got := toSDKInputSchema(schema)
	assert.Equal(t, []string{"path"}, got.Required)
	assert.Equal(t, schema["properties"], got.Properties)
	assert.Equal(t, false, got.ExtraFields["additionalProperties"])
	assert.Equal(t, schema["$defs"], got.ExtraFields["$defs"])
	_, hasType := got.ExtraFields["type"]
	assert.False(t, hasType, "type is the SDK's own constant field, never an extra")

	// Round-trip through JSON: the wire shape must carry required.
	blob, err := json.Marshal(got)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(blob, &wire))
	assert.Equal(t, []any{"path"}, wire["required"])
	assert.Equal(t, false, wire["additionalProperties"])
	assert.Equal(t, "object", wire["type"])
}

func TestToSDKInputSchema_RequiredFromDecodedJSON(t *testing.T) {
	// A schema that went through encoding/json carries []any, not
	// []string. Both spellings must survive.
	got := toSDKInputSchema(map[string]any{"required": []any{"a", "b", 7}})
	assert.Equal(t, []string{"a", "b"}, got.Required)
	assert.Nil(t, toSDKInputSchema(map[string]any{}).Required)
	assert.Nil(t, toSDKInputSchema(map[string]any{}).ExtraFields)
}
