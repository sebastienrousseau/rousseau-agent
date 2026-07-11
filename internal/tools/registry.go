package tools

import (
	"fmt"
	"sync"
)

// Registry is a concurrency-safe lookup table of Tools keyed by Name.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a Tool. Duplicate names return an error.
func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("tools: duplicate registration for %q", name)
	}
	r.tools[name] = t
	return nil
}

// MustRegister is Register that panics on error. Reserved for package-init
// wiring in main, never called from library code.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get looks a Tool up by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names returns the sorted set of registered tool names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for k := range r.tools {
		out = append(out, k)
	}
	return out
}

// Definitions renders every registered Tool as a wire-shape Definition
// ready to hand to a Provider.
func (r *Registry) Definitions() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Definition, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, Definition{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return out
}
