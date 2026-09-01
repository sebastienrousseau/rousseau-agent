package fuzztest_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/testutil/fuzztest"
)

func TestNew_DeterministicAcrossRuns(t *testing.T) {
	// Same test name + no seed override → identical output. This
	// is the reproducibility contract; if it breaks, "reproduce
	// the CI failure locally" breaks.
	var a, b string
	fuzztest.New(t).Fuzz(&a)
	fuzztest.New(t).Fuzz(&b)
	assert.Equal(t, a, b, "same test name must yield identical fuzz output")
}

func TestNew_DifferentSeedsProduceDifferentValues(t *testing.T) {
	var a, b string
	fuzztest.New(t, fuzztest.WithSeed(1)).Fuzz(&a)
	fuzztest.New(t, fuzztest.WithSeed(2)).Fuzz(&b)
	assert.NotEqual(t, a, b)
}

func TestNew_EnvSeedOverride(t *testing.T) {
	t.Setenv(fuzztest.SeedEnvVar, "42")
	var a string
	fuzztest.New(t).Fuzz(&a)
	// Same seed via env → matches an explicit WithSeed(42).
	var b string
	fuzztest.New(t, fuzztest.WithSeed(42)).Fuzz(&b)
	assert.Equal(t, a, b)
}

func TestNew_EnvSeedGarbageFallsThroughToNameDerived(t *testing.T) {
	t.Setenv(fuzztest.SeedEnvVar, "not-an-int")
	// Should not panic; should fall back to name-derived seed.
	var a string
	fuzztest.New(t).Fuzz(&a)
	assert.NotEmpty(t, a)
}

func TestNew_NilTBPanics(t *testing.T) {
	assert.Panics(t, func() { _ = fuzztest.New(nil) })
}

func TestTimePopulator_JSONRoundtrips(t *testing.T) {
	// The custom time.Time populator MUST survive JSON round-
	// trip — that's the whole reason it exists. gofuzz's default
	// leaves time.Time zero-valued because it can't touch
	// unexported fields.
	f := fuzztest.New(t)
	for i := 0; i < 100; i++ {
		var got time.Time
		f.Fuzz(&got)
		require.False(t, got.IsZero(), "time.Time must never be zero after populator")
		require.Equal(t, time.UTC, got.Location())
		b, err := json.Marshal(got)
		require.NoError(t, err)
		var back time.Time
		require.NoError(t, json.Unmarshal(b, &back))
		// JSON encodes RFC 3339 with nanosecond precision; the
		// populator uses second precision so equality is exact.
		assert.True(t, got.Equal(back))
	}
}

func TestStringPopulator_LengthAndPrintable(t *testing.T) {
	f := fuzztest.New(t)
	for i := 0; i < 100; i++ {
		var s string
		f.Fuzz(&s)
		assert.GreaterOrEqual(t, len(s), 8)
		assert.LessOrEqual(t, len(s), 64)
		for _, r := range s {
			assert.True(t, r >= 0x20 && r < 0x7F, "non-printable rune %U in %q", r, s)
		}
	}
}

func TestOptions_NilChanceAllowsNilPointers(t *testing.T) {
	// With NilChance=1.0 every pointer field is left nil. Proves
	// the option threads through.
	f := fuzztest.New(t, fuzztest.WithNilChance(1.0))
	type box struct {
		P *int
	}
	var seenNil bool
	for i := 0; i < 20; i++ {
		var b box
		f.Fuzz(&b)
		if b.P == nil {
			seenNil = true
			break
		}
	}
	assert.True(t, seenNil)
}

func TestOptions_MaxDepthAndSliceRange(t *testing.T) {
	// Smoke: option constructors do not panic and their fuzzers
	// produce non-empty output.
	f := fuzztest.New(t, fuzztest.WithMaxDepth(2), fuzztest.WithSliceRange(2, 3))
	var s []string
	f.Fuzz(&s)
	assert.GreaterOrEqual(t, len(s), 2)
	assert.LessOrEqual(t, len(s), 3)
}
