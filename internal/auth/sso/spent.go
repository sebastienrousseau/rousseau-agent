package sso

import (
	"context"
	"sync"
	"time"
)

// TokenSpender records the tokens used to /login so each token signs
// a user in once. A token pasted where others can read it, or lifted
// from a log, cannot then be replayed by another handle.
type TokenSpender interface {
	// Spend records key until expiresAt and reports whether it was
	// unspent. Implementations drop expired keys.
	Spend(ctx context.Context, key string, expiresAt time.Time) (bool, error)
}

// MemorySpender is an in-process [TokenSpender] for stores without a
// table of their own. Spent keys are forgotten on restart.
type MemorySpender struct {
	mu    sync.Mutex
	spent map[string]time.Time
	now   func() time.Time
}

// Spend satisfies [TokenSpender].
func (m *MemorySpender) Spend(_ context.Context, key string, expiresAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	if m.spent == nil {
		m.spent = map[string]time.Time{}
	}
	for k, exp := range m.spent {
		if !exp.After(now) {
			delete(m.spent, k)
		}
	}
	if _, ok := m.spent[key]; ok {
		return false, nil
	}
	m.spent[key] = expiresAt
	return true, nil
}
