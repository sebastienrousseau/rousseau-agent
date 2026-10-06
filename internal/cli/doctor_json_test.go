package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderJSON_WorstStatusWins(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderJSON(&buf, []diagResult{
		{Name: "a", Status: "ok", Detail: "x"},
		{Name: "b", Status: "warn", Detail: "y"},
	}))
	var rep doctorReport
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rep))
	assert.Equal(t, "warn", rep.Status)
	assert.Len(t, rep.Checks, 2)
	assert.Equal(t, "b", rep.Checks[1].Name)

	buf.Reset()
	require.NoError(t, renderJSON(&buf, []diagResult{{Name: "c", Status: "fail"}, {Name: "d", Status: "warn"}}))
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rep))
	assert.Equal(t, "fail", rep.Status)

	buf.Reset()
	require.NoError(t, renderJSON(&buf, nil))
	assert.Contains(t, buf.String(), `"checks": []`)
	assert.Contains(t, buf.String(), `"status": "ok"`)
}

func TestFailedChecks_NamesEveryFailure(t *testing.T) {
	assert.Nil(t, failedChecks([]diagResult{{Name: "a", Status: "ok"}}))
	assert.Equal(t, []string{"x", "z"}, failedChecks([]diagResult{
		{Name: "x", Status: "fail"}, {Name: "y", Status: "warn"}, {Name: "z", Status: "fail"},
	}))
}
