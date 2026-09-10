package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestRun_DemoExitsSuccess exercises the demo end-to-end: it starts
// the server, drives the v1 client through the four spec surfaces,
// and confirms the exit code is 0 and every expected line lands on
// stdout.
func TestRun_DemoExitsSuccess(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	code := run(context.Background(), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0. errOut:\n%s", code, errOut.String())
	}
	got := out.String()
	// Freeze the four observable outputs — a regression on any spec
	// surface will drop the corresponding line and fail here.
	for _, want := range []string{
		"agent card:",
		"protocol=1.0",
		"submitted:",
		"state=TASK_STATE_SUBMITTED",
		"stream:",
		"state=TASK_STATE_COMPLETED",
		"cancelled:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
}
