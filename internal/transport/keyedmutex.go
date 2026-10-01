package transport

import "sync"

// keyedMutex serialises work per key (a sender) without serialising
// different keys against each other. Entries are reference-counted and
// dropped when unused, so memory tracks active senders only. The zero
// value is ready to use.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyedEntry
}

type keyedEntry struct {
	sync.Mutex
	refs int
}

// Lock acquires key's lock and returns its unlock function.
func (k *keyedMutex) Lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &keyedEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()

	e.Lock()
	return func() {
		e.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}
