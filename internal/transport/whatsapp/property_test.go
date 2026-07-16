package whatsapp

import (
	"strings"
	"testing"
	"testing/quick"
)

// TestProperty_PrependHeader_neverPanics is a randomised property
// test. PrependHeader is not idempotent by design (each call
// prepends), so the strongest invariant is: no panic on any input,
// and the original text always appears in the output.
func TestProperty_PrependHeader_alwaysContainsBody(t *testing.T) {
	prop := func(text, header string) bool {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on text=%q header=%q: %v", text, header, r)
			}
		}()
		out := PrependHeader(text, header)
		return strings.Contains(out, text)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}
