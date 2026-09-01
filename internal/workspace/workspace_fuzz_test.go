package workspace_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/testutil/fuzztest"
	"github.com/sebastienrousseau/rousseau-agent/internal/workspace"
)

// TestFuzz_ConfigForRoundtrip: for every Registry built from a
// list of Configs, `ConfigFor(c.ID) == c` must hold for each
// input. Property: registry preserves configs by ID.
func TestFuzz_ConfigForRoundtrip(t *testing.T) {
	f := fuzztest.New(t)
	for i := 0; i < fuzztest.DefaultIterations; i++ {
		// Generate a list with unique IDs — dupes are explicitly
		// rejected by the constructor and would abort the round-
		// trip we're testing. IDs are indexed so uniqueness is
		// trivial without needing a set.
		var count uint8
		f.Fuzz(&count)
		n := int(count%8) + 1
		configs := make([]workspace.Config, n)
		for j := range configs {
			f.Fuzz(&configs[j])
			configs[j].ID = workspace.ID(fmt.Sprintf("ws-%d-%d", i, j))
		}

		reg, err := workspace.NewMapResolver(configs)
		require.NoError(t, err)

		for _, want := range configs {
			got, ok := reg.ConfigFor(want.ID)
			require.True(t, ok, "ConfigFor(%q) must return the config we registered", want.ID)
			require.Equal(t, want, got, "ConfigFor must preserve the exact Config")
		}
	}
}

// TestFuzz_AllPreservesOrderAndCount: All() returns every input
// config in insertion order. Property: no drops, no reorders, no
// silent dedup.
func TestFuzz_AllPreservesOrderAndCount(t *testing.T) {
	f := fuzztest.New(t)
	for i := 0; i < fuzztest.DefaultIterations; i++ {
		var count uint8
		f.Fuzz(&count)
		n := int(count%16) + 1
		configs := make([]workspace.Config, n)
		for j := range configs {
			f.Fuzz(&configs[j])
			configs[j].ID = workspace.ID(fmt.Sprintf("ws-%d-%d", i, j))
		}

		reg, err := workspace.NewMapResolver(configs)
		require.NoError(t, err)

		got := reg.All()
		require.Equal(t, len(configs), len(got))
		for j := range configs {
			require.Equal(t, configs[j], got[j], "insertion order must be preserved at index %d", j)
		}
	}
}

// TestFuzz_ResolveIsDeterministic: for the same registry, calling
// Resolve with the same (transport, sender) twice returns the
// same ID. Property: no hidden randomness in the resolver.
func TestFuzz_ResolveIsDeterministic(t *testing.T) {
	f := fuzztest.New(t)
	ctx := context.Background()
	for i := 0; i < fuzztest.DefaultIterations; i++ {
		var count uint8
		f.Fuzz(&count)
		n := int(count%8) + 1
		configs := make([]workspace.Config, n)
		for j := range configs {
			f.Fuzz(&configs[j])
			configs[j].ID = workspace.ID(fmt.Sprintf("ws-%d-%d", i, j))
		}
		reg, err := workspace.NewMapResolver(configs)
		require.NoError(t, err)

		var transport, sender string
		f.Fuzz(&transport)
		f.Fuzz(&sender)

		a, err := reg.Resolve(ctx, transport, sender)
		require.NoError(t, err)
		b, err := reg.Resolve(ctx, transport, sender)
		require.NoError(t, err)
		require.Equal(t, a, b, "resolve must be deterministic")
	}
}

// TestFuzz_WithIDFromContextRoundtrip: for every non-empty ID,
// WithID → FromContext returns the same ID. Property: ctx-value
// storage is lossless.
func TestFuzz_WithIDFromContextRoundtrip(t *testing.T) {
	f := fuzztest.New(t)
	for i := 0; i < fuzztest.DefaultIterations; i++ {
		var raw string
		f.Fuzz(&raw)
		if raw == "" {
			continue // empty is a documented no-op
		}
		id := workspace.ID(raw)
		ctx := workspace.WithID(context.Background(), id)
		require.Equal(t, id, workspace.FromContext(ctx))
	}
}

// TestFuzz_DuplicateIDsAlwaysRejected: any list containing two
// configs with the same ID must fail construction. Property: no
// silent last-write-wins on collisions.
func TestFuzz_DuplicateIDsAlwaysRejected(t *testing.T) {
	f := fuzztest.New(t)
	for i := 0; i < 200; i++ {
		var (
			dup      workspace.Config
			filler   workspace.Config
			position uint8
		)
		f.Fuzz(&dup)
		f.Fuzz(&filler)
		f.Fuzz(&position)
		dup.ID = "collision"
		filler.ID = workspace.ID(fmt.Sprintf("filler-%d", i))
		configs := []workspace.Config{dup, filler}
		// Insert a second collision at a random position — the
		// first-match constructor loop must catch it wherever it
		// lands.
		configs = append(configs, workspace.Config{ID: "collision"})
		if position%2 == 0 {
			// Reverse the slice to shift the duplicate location.
			for l, r := 0, len(configs)-1; l < r; l, r = l+1, r-1 {
				configs[l], configs[r] = configs[r], configs[l]
			}
		}
		_, err := workspace.NewMapResolver(configs)
		require.Error(t, err)
		require.Contains(t, err.Error(), "duplicate")
	}
}

// TestFuzz_EmptyIDAlwaysRejected: any config with an empty ID
// must fail construction. Property: the "ID is required" guard
// covers every entry, not just the first.
func TestFuzz_EmptyIDAlwaysRejected(t *testing.T) {
	f := fuzztest.New(t)
	for i := 0; i < 200; i++ {
		var count uint8
		f.Fuzz(&count)
		n := int(count%5) + 1
		configs := make([]workspace.Config, n)
		for j := range configs {
			f.Fuzz(&configs[j])
			configs[j].ID = workspace.ID(fmt.Sprintf("ok-%d-%d", i, j))
		}
		// Pick a random position to blank out.
		var blankAt uint8
		f.Fuzz(&blankAt)
		configs[int(blankAt)%len(configs)].ID = ""

		_, err := workspace.NewMapResolver(configs)
		require.Error(t, err)
		require.Contains(t, err.Error(), "ID")
	}
}
