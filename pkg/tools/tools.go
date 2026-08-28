// Package tools re-exports the internal/tools registry surface so
// external modules can compose their own tool set without importing
// /internal.
package tools

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// Tool aliases [tools.Tool].
type Tool = tools.Tool

// Registry aliases [tools.Registry].
type Registry = tools.Registry

// Definition aliases [tools.Definition].
type Definition = tools.Definition

// NewRegistry constructs an empty [Registry]. Alias for
// [tools.NewRegistry].
func NewRegistry() *Registry { return tools.NewRegistry() }
