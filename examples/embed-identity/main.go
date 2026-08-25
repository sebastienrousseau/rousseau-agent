// Package main demonstrates the identity resolver (internal/identity
// + internal/state/sqlite/identity.go). Two handles on different
// transports get linked to a single identity; a subsequent Resolve
// on either handle returns the same ID — the primitive that makes
// cross-transport session continuity possible.
//
// Run with:
//
//	go run ./examples/embed-identity
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func main() { os.Exit(run(context.Background(), os.Stdout, os.Stderr)) }

// run executes the demo and returns the process exit code. main does
// nothing but call os.Exit so that tests can drive run directly.
func run(ctx context.Context, out, errOut io.Writer) int {
	if err := demo(ctx, out); err != nil {
		fmt.Fprintln(errOut, "embed-identity:", err)
		return 1
	}
	return 0
}

func demo(ctx context.Context, out io.Writer) error {
	base, err := sqlitestore.Open(ctx, ":memory:")
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = base.Close() }()

	ids, err := sqlitestore.NewIdentityStore(ctx, base)
	if err != nil {
		return fmt.Errorf("identity store: %w", err)
	}

	// First inbound message from WhatsApp handle — auto-provisions.
	id, err := ids.Provision(ctx, "whatsapp", "+447906009073", "Alice")
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	fmt.Fprintf(out, "provisioned identity for whatsapp:+447906009073 → %s\n", id)

	// Alice sends a /link slack:U01234 command later; the router
	// resolves her identity and calls Link on the resolver.
	if err := ids.Link(ctx, id, "slack", "U01234"); err != nil {
		return fmt.Errorf("link: %w", err)
	}
	fmt.Fprintln(out, "linked slack:U01234 to Alice's identity")

	// Now BOTH handles resolve to the same identity — the session
	// store keys on this ID, so a session started on WhatsApp
	// continues on Slack.
	whatsapp, _ := ids.Resolve(ctx, "whatsapp", "+447906009073")
	slack, _ := ids.Resolve(ctx, "slack", "U01234")
	fmt.Fprintf(out, "whatsapp resolves to %s\n", whatsapp)
	fmt.Fprintf(out, "slack    resolves to %s\n", slack)
	fmt.Fprintf(out, "same identity: %v\n", whatsapp == slack)

	// Inspect the full record.
	rec, _ := ids.Get(ctx, id)
	fmt.Fprintf(out, "\nfull identity record:\n")
	fmt.Fprintf(out, "  ID:      %s\n", rec.ID)
	fmt.Fprintf(out, "  Display: %s\n", rec.PrimaryDisplay)
	fmt.Fprintf(out, "  Handles: %d\n", len(rec.Handles))
	for _, h := range rec.Handles {
		fmt.Fprintf(out, "    %s:%s\n", h.Transport, h.Sender)
	}
	return nil
}
