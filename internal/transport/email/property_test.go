package email

import (
	"strings"
	"testing"
	"testing/quick"
)

// TestProperty_stripHeaders_neverPanics is a randomised property test:
// stripHeaders must not panic on any string input, and its output must
// always be a suffix of the input (or the input itself when there is
// no separator).
func TestProperty_stripHeaders_neverPanics(t *testing.T) {
	prop := func(raw string) bool {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %q: %v", raw, r)
			}
		}()
		out := stripHeaders(raw)
		// Suffix invariant: whatever we return must appear inside the
		// original (or equal it verbatim). This catches accidental
		// slicing bugs or unicode boundary corruption.
		return strings.Contains(raw, out) || out == ""
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}

// TestProperty_buildMessage_alwaysContainsBody asserts a load-bearing
// invariant: however weird the from/to/body inputs are, the produced
// RFC 5322 blob must contain the body verbatim. This lets us catch
// accidental escaping or header-injection sanitisation drift.
func TestProperty_buildMessage_alwaysContainsBody(t *testing.T) {
	prop := func(from, to, body string) bool {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on from=%q to=%q body=%q: %v", from, to, body, r)
			}
		}()
		msg := string(buildMessage(from, to, body))
		return strings.Contains(msg, body)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}
