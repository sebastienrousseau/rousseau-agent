package sqlite_test

import (
	"context"
	"testing"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// BenchmarkResolve runs on every inbound message when the transport
// router has an Identity resolver wired — must be sub-millisecond.
func BenchmarkResolve_HitHot(b *testing.B) {
	ctx := context.Background()
	base, err := sqlitestore.Open(ctx, ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = base.Close() }() //nolint:errcheck // bench cleanup
	r, err := sqlitestore.NewIdentityStore(ctx, base)
	if err != nil {
		b.Fatal(err)
	}
	// Preload one identity + handle.
	if _, err := r.Provision(ctx, "whatsapp", "+123", "Alice"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Resolve(ctx, "whatsapp", "+123") //nolint:errcheck // bench measures resolve cost
	}
}

// BenchmarkResolve_Miss measures the not-linked case — inbound
// message from a fresh sender that hasn't been provisioned yet.
func BenchmarkResolve_Miss(b *testing.B) {
	ctx := context.Background()
	base, err := sqlitestore.Open(ctx, ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = base.Close() }() //nolint:errcheck // bench cleanup
	r, err := sqlitestore.NewIdentityStore(ctx, base)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Resolve(ctx, "whatsapp", "+never-linked") //nolint:errcheck // bench measures resolve cost
	}
}
