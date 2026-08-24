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
	"os"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func main() {
	ctx := context.Background()

	base, err := sqlitestore.Open(ctx, ":memory:")
	must(err)
	defer func() { _ = base.Close() }()

	ids, err := sqlitestore.NewIdentityStore(ctx, base)
	must(err)

	// First inbound message from WhatsApp handle — auto-provisions.
	id, err := ids.Provision(ctx, "whatsapp", "+447906009073", "Alice")
	must(err)
	fmt.Printf("provisioned identity for whatsapp:+447906009073 → %s\n", id)

	// Alice sends a /link slack:U01234 command later; the router
	// resolves her identity and calls Link on the resolver.
	must(ids.Link(ctx, id, "slack", "U01234"))
	fmt.Println("linked slack:U01234 to Alice's identity")

	// Now BOTH handles resolve to the same identity — the session
	// store keys on this ID, so a session started on WhatsApp
	// continues on Slack.
	whatsapp, _ := ids.Resolve(ctx, "whatsapp", "+447906009073")
	slack, _ := ids.Resolve(ctx, "slack", "U01234")
	fmt.Printf("whatsapp resolves to %s\n", whatsapp)
	fmt.Printf("slack    resolves to %s\n", slack)
	fmt.Printf("same identity: %v\n", whatsapp == slack)

	// Inspect the full record.
	rec, _ := ids.Get(ctx, id)
	fmt.Printf("\nfull identity record:\n")
	fmt.Printf("  ID:      %s\n", rec.ID)
	fmt.Printf("  Display: %s\n", rec.PrimaryDisplay)
	fmt.Printf("  Handles: %d\n", len(rec.Handles))
	for _, h := range rec.Handles {
		fmt.Printf("    %s:%s\n", h.Transport, h.Sender)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
