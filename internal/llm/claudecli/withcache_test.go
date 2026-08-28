package claudecli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type stubCache struct {
	known    map[string]bool
	remember []string
}

func (s *stubCache) IsKnown(id string) bool { return s.known[id] }
func (s *stubCache) Remember(id string) {
	s.remember = append(s.remember, id)
	if s.known == nil {
		s.known = map[string]bool{}
	}
	s.known[id] = true
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

func TestInMemorySessionCache_IdempotentRemember(t *testing.T) {
	c := NewInMemorySessionCache()
	assert.False(t, c.IsKnown("a"))
	c.Remember("a")
	c.Remember("a")
	assert.True(t, c.IsKnown("a"))
}
