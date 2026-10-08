package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// limitBackend records the limit each tool passed to the store.
type limitBackend struct {
	fakeBackend
	searchLimit, listLimit int
}

func (b *limitBackend) Search(_ context.Context, _ string, o sqlitestore.SearchOptions) ([]sqlitestore.SearchHit, error) {
	b.searchLimit = o.Limit
	return nil, nil
}

func (b *limitBackend) List(_ context.Context, limit int) ([]state.Summary, error) {
	b.listLimit = limit
	return nil, nil
}

// L-13: the session tools clamp limit to 1..200 (absent = 20) and
// declare those bounds in the schema. Before the fix a negative or
// absent list limit listed every session.
func TestSessionTools_ClampLimit(t *testing.T) {
	cases := map[string]int{`{}`: 20, `{"limit":-5}`: 1, `{"limit":7}`: 7, `{"limit":1000}`: 200}
	for args, want := range cases {
		be := &limitBackend{}
		_, err := callTool(t, listSessionsTool(be), args)
		require.NoError(t, err)
		assert.Equal(t, want, be.listLimit, "list %s", args)

		q := `{"query":"x",` + args[1:]
		if args == `{}` {
			q = `{"query":"x"}`
		}
		_, err = callTool(t, searchSessionsTool(be), q)
		require.NoError(t, err)
		assert.Equal(t, want, be.searchLimit, "search %s", q)
	}
}

func TestSessionTools_SchemaDeclaresLimitBounds(t *testing.T) {
	be := &limitBackend{}
	for _, spec := range []ToolSpec{listSessionsTool(be), searchSessionsTool(be)} {
		var schema struct {
			Properties map[string]struct {
				Minimum *int `json:"minimum"`
				Maximum *int `json:"maximum"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(spec.InputSchema, &schema))
		lim := schema.Properties["limit"]
		require.NotNil(t, lim.Minimum, spec.Name)
		require.NotNil(t, lim.Maximum, spec.Name)
		assert.Equal(t, 1, *lim.Minimum, spec.Name)
		assert.Equal(t, 200, *lim.Maximum, spec.Name)
	}
}
