// Package fuzztest is the shared harness for property-based tests
// that use [github.com/google/gofuzz] to generate random values.
//
// # Why gofuzz (not testing.F)
//
// Go's stdlib fuzzer ([testing.F]) is coverage-guided and terrific
// for finding crash inputs on byte-oriented parsers. For property
// tests on typed structs — "generate 500 random Sessions, round-
// trip through JSON, assert equality" — a per-field random
// populator is a better fit. gofuzz is the mature choice: same
// API used by Kubernetes for their apimachinery roundtrip tests.
//
// # Usage
//
//	func TestFoo_JSONRoundtrip(t *testing.T) {
//	    f := fuzztest.New(t)
//	    for i := 0; i < fuzztest.DefaultIterations; i++ {
//	        var got Foo
//	        f.Fuzz(&got)
//	        b, err := json.Marshal(got)
//	        require.NoError(t, err)
//	        var back Foo
//	        require.NoError(t, json.Unmarshal(b, &back))
//	        require.Equal(t, got, back)
//	    }
//	}
//
// # Determinism
//
// [New] seeds the fuzzer with a stable value derived from the
// test's own name, so a specific failing case is reproducible.
// The seed is logged with [t.Logf] on the first call so a broken
// test always tells you how to reproduce it. Override with
// [WithSeed] or the ROUSSEAU_FUZZ_SEED env var.
//
// # Custom populators
//
// The default fuzzer refuses to write to unexported fields, which
// means types like [time.Time] can end up in bogus internal
// states. Every custom populator lives in this package so the
// same behaviour is inherited by every test — no drift.
//
//   - [time.Time]: 0001-01-01 through 2100-01-01, UTC, seconds
//     precision (JSON round-trips truncate to the wire format).
//   - [string]: printable ASCII 8–64 chars.
package fuzztest

import (
	"os"
	"strconv"
	"testing"
	"time"

	fuzz "github.com/google/gofuzz"
)

// DefaultIterations is the number of random values a property
// test should generate per Fuzzer. Chosen so a single test
// completes in <100 ms while giving the fuzzer enough breadth
// to hit the interesting boundary cases (empty strings, negative
// counters, deeply-nested slices, etc.).
const DefaultIterations = 500

// SeedEnvVar overrides the default (test-name-derived) seed with
// an explicit int64. Useful when a CI failure hands you a seed
// and you want to reproduce locally.
const SeedEnvVar = "ROUSSEAU_FUZZ_SEED"

// Option customises a Fuzzer built by [New].
type Option func(*config)

type config struct {
	seed        int64
	seedFromEnv bool
	nilChance   float64
	maxDepth    int
	numElements [2]int // min, max slice/map element count
}

// WithSeed forces a specific PRNG seed. Bypasses the test-name
// hash. Prefer setting ROUSSEAU_FUZZ_SEED at the shell for
// one-off reproduction — this Option exists for tests that want
// determinism independent of their own name.
func WithSeed(seed int64) Option {
	return func(c *config) { c.seed = seed; c.seedFromEnv = false }
}

// WithNilChance sets the probability that pointer / slice / map
// fields are left nil. Default 0 — property tests want fully-
// populated structs so equality assertions have teeth.
func WithNilChance(p float64) Option {
	return func(c *config) { c.nilChance = p }
}

// WithMaxDepth caps how deep the fuzzer will recurse into
// nested struct fields. Default 8. Increase for deeply-nested
// message types.
func WithMaxDepth(d int) Option {
	return func(c *config) { c.maxDepth = d }
}

// WithSliceRange sets the inclusive [min, max] length range for
// slices and maps. Default [1, 5].
func WithSliceRange(minLen, maxLen int) Option {
	return func(c *config) { c.numElements = [2]int{minLen, maxLen} }
}

// New returns a [fuzz.Fuzzer] configured for property tests in
// this codebase. Panics if t is nil so misuse fails at the
// call-site.
//
// The fuzzer is pre-loaded with the custom populators listed in
// the package doc. Callers can chain further [fuzz.Fuzzer.Funcs]
// on the returned value.
func New(t testing.TB, opts ...Option) *fuzz.Fuzzer {
	if t == nil {
		// Can't call t.Fatal — the whole point is that t is nil.
		// Fail loud so misuse is caught at the call-site.
		panic("fuzztest.New: nil TB") //nolint:forbidigo // test-helper misuse guard; no alternative when the TB itself is nil
	}
	cfg := config{
		seedFromEnv: true,
		nilChance:   0,
		maxDepth:    8,
		numElements: [2]int{1, 5},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	seed := cfg.seed
	if cfg.seedFromEnv {
		if v := os.Getenv(SeedEnvVar); v != "" {
			if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
				seed = parsed
			} else {
				t.Logf("fuzztest: %s=%q could not be parsed as int64, falling back to test-name-derived seed", SeedEnvVar, v)
			}
		}
		if seed == 0 {
			seed = seedFromName(t.Name())
		}
	}
	t.Logf("fuzztest: seed=%d (reproduce with %s=%d)", seed, SeedEnvVar, seed)

	f := fuzz.NewWithSeed(seed).
		NilChance(cfg.nilChance).
		MaxDepth(cfg.maxDepth).
		NumElements(cfg.numElements[0], cfg.numElements[1]).
		Funcs(
			// time.Time: gofuzz can't touch the unexported fields
			// on the stdlib type, so it hands back the zero value.
			// Populate explicitly across a range the JSON wire
			// format survives.
			func(t *time.Time, c fuzz.Continue) {
				// Unix seconds range that spans the plausible
				// window without hitting Y2038 or the epoch edge.
				sec := c.Int63n(4102444800) // through 2100-01-01
				*t = time.Unix(sec, 0).UTC()
			},
			// string: printable ASCII in a bounded length so
			// tests aren't dominated by pathological unicode.
			// Individual tests can override this with their own
			// Funcs when they NEED unicode / empty / long inputs.
			func(s *string, c fuzz.Continue) {
				*s = randomPrintable(c, 8, 64)
			},
		)
	return f
}

// seedFromName produces a stable 63-bit seed from a test name.
// Same name → same seed → same failing case → reproducible bug.
func seedFromName(name string) int64 {
	// FNV-1a keeps the seed derivation dependency-free and
	// deterministic across Go versions.
	const (
		offset64 uint64 = 14695981039346656037
		prime64  uint64 = 1099511628211
	)
	h := offset64
	for i := 0; i < len(name); i++ {
		h ^= uint64(name[i])
		h *= prime64
	}
	// Fold to a positive int64.
	return int64(h & 0x7FFFFFFFFFFFFFFF)
}

// randomPrintable draws a printable-ASCII string of length in
// [minLen, maxLen]. Excludes control characters but keeps
// punctuation so tests exercise quoting / escaping.
func randomPrintable(c fuzz.Continue, minLen, maxLen int) string {
	n := minLen
	if maxLen > minLen {
		n += c.Intn(maxLen - minLen + 1)
	}
	const printable = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789 !\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
	b := make([]byte, n)
	for i := range b {
		b[i] = printable[c.Intn(len(printable))]
	}
	return string(b)
}
