package claudecli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type stubCache struct {
	known    map[string]bool
	remember []string
	forgot   []string
}

func (s *stubCache) IsKnown(id string) bool { return s.known[id] }
func (s *stubCache) Remember(id string) {
	s.remember = append(s.remember, id)
	if s.known == nil {
		s.known = map[string]bool{}
	}
	s.known[id] = true
}
func (s *stubCache) Forget(id string) {
	s.forgot = append(s.forgot, id)
	delete(s.known, id)
}

func TestWithCache_SwapsImplementation(t *testing.T) {
	p := New(Config{})
	sc := &stubCache{}
	got := p.WithCache(sc)
	assert.Same(t, p, got, "WithCache should return the same Provider")
	p.rememberSession("x")
	assert.Contains(t, sc.remember, "x")
	assert.True(t, p.knowsSession("x"))
}

func TestWithCache_NilLeavesDefault(t *testing.T) {
	p := New(Config{})
	got := p.WithCache(nil)
	assert.Same(t, p, got)
	p.rememberSession("y")
	assert.True(t, p.knowsSession("y"))
}

// TestInMemorySessionCache_Forget locks in the Forget semantics
// added for the session-in-use recovery path: after Forget, IsKnown
// returns false so the next Stream call passes --session-id (creates
// a fresh transcript) rather than --resume (which would fail against
// a rotated transcript). Forget on an unknown id is a no-op.
func TestInMemorySessionCache_Forget(t *testing.T) {
	c := NewInMemorySessionCache()
	c.Remember("a")
	c.Remember("b")
	assert.True(t, c.IsKnown("a"))
	assert.True(t, c.IsKnown("b"))

	c.Forget("a")
	assert.False(t, c.IsKnown("a"), "Forget must remove the id")
	assert.True(t, c.IsKnown("b"), "Forget must not affect siblings")

	// Idempotent: forgetting an unknown id is a no-op.
	assert.NotPanics(t, func() { c.Forget("never-seen") })
	assert.NotPanics(t, func() { c.Forget("a") }) // already forgotten
}

func TestInMemorySessionCache_IdempotentRemember(t *testing.T) {
	c := NewInMemorySessionCache()
	assert.False(t, c.IsKnown("a"))
	c.Remember("a")
	c.Remember("a")
	assert.True(t, c.IsKnown("a"))
}
