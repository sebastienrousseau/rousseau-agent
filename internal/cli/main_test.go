package cli

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points XDG_DATA_HOME at a throwaway directory for the whole
// package. Transport commands under test start real heartbeats (and
// resolve other data paths) from it; without this they wrote into the
// developer's ~/.local/share/rousseau, which a live daemon may mount.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rousseau-cli-test-data-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cli tests: temp data dir:", err)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_DATA_HOME", dir); err != nil {
		fmt.Fprintln(os.Stderr, "cli tests: set XDG_DATA_HOME:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a temp dir
	os.Exit(code)
}
