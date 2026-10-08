package fsguard

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// L-10: the daemon generates the audit chain key under
// $XDG_STATE_HOME/rousseau when that is set, so the deny list must
// cover it there too, not only under ~/.local/state.
func TestDefaultDeny_CoversXDGStateHome(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	assert.Contains(t, DefaultDeny(), filepath.Join(state, "rousseau"))

	g, err := New("", nil)
	assert.NoError(t, err)
	_, err = g.Resolve(filepath.Join(state, "rousseau", "audit-chain.key"))
	assert.Error(t, err)
}
