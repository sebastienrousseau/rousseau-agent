package media_test

import (
	"errors"
	"fmt"
	"os"

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
)

// ExamplePolicy_Accept demonstrates the transport-side pattern: the
// bytes arrive from an untrusted peer, the policy sniffs them, and
// only accepted payloads become an agent.ContentImage block.
func ExamplePolicy_Accept() {
	data, _ := os.ReadFile("testdata/screenshot.png") //nolint:errcheck // example: fallback to inline PNG below
	if data == nil {
		// example file absent in this test env; use inline PNG header
		data = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	}
	policy := media.Policy{}
	mime, err := policy.Accept(data, 0)
	if err != nil {
		if errors.Is(err, media.ErrTooLarge) {
			fmt.Println("attachment too large")
			return
		}
		fmt.Println("rejected:", err)
		return
	}
	fmt.Println("accepted:", mime)
	// Output: accepted: image/png
}
