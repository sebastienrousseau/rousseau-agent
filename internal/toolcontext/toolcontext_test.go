package toolcontext_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/toolcontext"
)

func TestSession_RoundTrip(t *testing.T) {
	type fakeSession struct{ ID string }
	s := &fakeSession{ID: "s1"}

	ctx := toolcontext.WithSession(context.Background(), s)
	raw, ok := toolcontext.Session(ctx)
	if !ok {
		t.Fatal("Session: ok=false, want true")
	}
	got, ok := raw.(*fakeSession)
	if !ok {
		t.Fatalf("Session: type = %T, want *fakeSession", raw)
	}
	if got.ID != "s1" {
		t.Errorf("Session.ID = %q, want s1", got.ID)
	}
}

func TestSession_Absent(t *testing.T) {
	_, ok := toolcontext.Session(context.Background())
	if ok {
		t.Error("Session: ok=true on empty context, want false")
	}
}

func TestSession_NilIsNoop(t *testing.T) {
	// WithSession(ctx, nil) must not overwrite an existing value.
	base := toolcontext.WithSession(context.Background(), "original")
	after := toolcontext.WithSession(base, nil)
	raw, ok := toolcontext.Session(after)
	if !ok {
		t.Fatal("Session: ok=false after WithSession(_, nil), want true (unchanged)")
	}
	if raw != "original" {
		t.Errorf("Session: got %v, want unchanged \"original\"", raw)
	}
}

func TestProvider_RoundTrip(t *testing.T) {
	type fakeProvider struct{ Name string }
	p := &fakeProvider{Name: "stub"}

	ctx := toolcontext.WithProvider(context.Background(), p)
	raw, ok := toolcontext.Provider(ctx)
	if !ok {
		t.Fatal("Provider: ok=false, want true")
	}
	got, ok := raw.(*fakeProvider)
	if !ok {
		t.Fatalf("Provider: type = %T, want *fakeProvider", raw)
	}
	if got.Name != "stub" {
		t.Errorf("Provider.Name = %q, want stub", got.Name)
	}
}

func TestLogger_RoundTrip(t *testing.T) {
	lg := slog.Default().With("test", "toolcontext")
	ctx := toolcontext.WithLogger(context.Background(), lg)
	got := toolcontext.Logger(ctx)
	if got == nil {
		t.Fatal("Logger: nil, want non-nil")
	}
	if got != lg {
		t.Error("Logger: returned different instance than was set")
	}
}

func TestLogger_DefaultsWhenAbsent(t *testing.T) {
	got := toolcontext.Logger(context.Background())
	if got == nil {
		t.Error("Logger: nil on empty context, want slog.Default")
	}
}

func TestLogger_NilIsNoop(t *testing.T) {
	lg := slog.Default().With("test", "toolcontext")
	base := toolcontext.WithLogger(context.Background(), lg)
	after := toolcontext.WithLogger(base, nil)
	if toolcontext.Logger(after) != lg {
		t.Error("Logger: WithLogger(_, nil) should not overwrite existing logger")
	}
}
