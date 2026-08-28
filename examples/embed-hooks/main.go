// Package main demonstrates the lifecycle-hook runner
// (internal/agent/hooks). Two shell-script hooks are compiled to
// temp files then wired to the pre_tool_use event. The first one
// allows the call; the second one denies commands containing
// "rm -rf" — a common minimum-viable safety net.
//
// Run with:
//
//	go run ./examples/embed-hooks
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
)

// The allow-hook always says allow. Real hooks might log for audit
// purposes and then say allow.
const allowScript = `#!/bin/sh
printf '{"decision":"allow"}'
`

// The deny-hook reads the payload from stdin and denies when the
// bash command's `command` field contains "rm -rf".
const denyScript = `#!/bin/sh
if grep -q 'rm -rf' -; then
    printf '{"decision":"deny","reason":"rm -rf blocked by policy"}'
else
    printf '{"decision":"allow"}'
fi
`

func main() { os.Exit(run(context.Background(), os.Stdout, os.Stderr)) }

// run executes the demo and returns the process exit code. main does
// nothing but call os.Exit so that tests can drive run directly.
func run(ctx context.Context, out, errOut io.Writer) int {
	if err := demo(ctx, out); err != nil {
		fmt.Fprintln(errOut, "embed-hooks:", err)
		return 1
	}
	return 0
}

func demo(ctx context.Context, out io.Writer) error {
	tmp, err := os.MkdirTemp("", "rousseau-hooks-example-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	allow := writeScript(tmp, "allow.sh", allowScript)
	deny := writeScript(tmp, "deny.sh", denyScript)

	set := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {
			{Name: "audit-only", Command: allow},
			{Name: "no-rm-rf", Command: deny},
		},
	}, slog.New(slog.NewTextHandler(out, nil)))

	// Two tool calls: one benign, one destructive.
	for _, cmd := range []string{"ls -la", "rm -rf /tmp/example"} {
		payload, _ := hooks.MarshalPreToolUse("s1", "bash", json.RawMessage(`{"command":"`+cmd+`"}`))
		v, _ := set.Run(ctx, hooks.EventPreToolUse, payload)
		fmt.Fprintf(out, "bash %-25s → %-6s reason=%q\n", cmd, v.Decision, v.Reason)
	}
	return nil
}

func writeScript(dir, name, body string) string {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec // example fixture
		panic(err)
	}
	return path
}
