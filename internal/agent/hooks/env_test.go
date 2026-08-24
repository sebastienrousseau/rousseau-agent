package hooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Cover the environment-merge helpers that hooks.runOne uses to
// layer per-hook Env on top of the daemon's own environment.
// These are unexported so they live in the same package (no _test
// suffix on the package name).

func TestSplitEnv_KeyValueSplit(t *testing.T) {
	k, v, ok := splitEnv("PATH=/usr/bin:/bin")
	assert.True(t, ok)
	assert.Equal(t, "PATH", k)
	assert.Equal(t, "/usr/bin:/bin", v)
}

func TestSplitEnv_EmptyValue(t *testing.T) {
	k, v, ok := splitEnv("EMPTY=")
	assert.True(t, ok)
	assert.Equal(t, "EMPTY", k)
	assert.Equal(t, "", v)
}

func TestSplitEnv_NoEqualsSign(t *testing.T) {
	k, v, ok := splitEnv("MALFORMED")
	assert.False(t, ok)
	assert.Equal(t, "MALFORMED", k)
	assert.Equal(t, "", v)
}

func TestMergeEnv_EmptyOverridesReturnsBase(t *testing.T) {
	base := []string{"A=1", "B=2"}
	got := mergeEnv(base, nil)
	assert.Equal(t, base, got)
}

func TestMergeEnv_OverridesReplaceBase(t *testing.T) {
	got := mergeEnv([]string{"PATH=/usr/bin", "HOME=/root"}, map[string]string{"HOME": "/home/rousseau"})
	// PATH preserved; HOME replaced.
	assert.Contains(t, got, "PATH=/usr/bin")
	assert.Contains(t, got, "HOME=/home/rousseau")
	assert.NotContains(t, got, "HOME=/root")
}

func TestMergeEnv_EmptyValueUnsetsVariable(t *testing.T) {
	got := mergeEnv([]string{"PATH=/usr/bin", "HOME=/root"}, map[string]string{"HOME": ""})
	assert.Contains(t, got, "PATH=/usr/bin")
	// HOME must be entirely removed — not "HOME=" or "HOME=/root".
	for _, kv := range got {
		assert.NotEqualf(t, "HOME=/root", kv, "HOME should have been unset, got %q", kv)
		assert.NotEqualf(t, "HOME=", kv, "unset HOME should not appear at all, got %q", kv)
	}
}

func TestMergeEnv_MalformedBaseEntriesPassThrough(t *testing.T) {
	// A base entry without "=" should not crash mergeEnv — it's
	// preserved as-is because splitEnv reports ok=false.
	got := mergeEnv([]string{"NOEQUALS", "A=1"}, map[string]string{"A": "2"})
	assert.Contains(t, got, "NOEQUALS")
	assert.Contains(t, got, "A=2")
}

func TestTrimForLog_LongStringTruncated(t *testing.T) {
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	got := trimForLog(string(long))
	assert.LessOrEqual(t, len(got), 205, "trimmed output should include an ellipsis")
	assert.Contains(t, got, "…")
}

func TestTrimForLog_NewlinesReplaced(t *testing.T) {
	got := trimForLog("line1\nline2\nline3")
	assert.Equal(t, "line1 line2 line3", got)
}
