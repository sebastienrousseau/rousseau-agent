// Package agent is the public library entry point for embedding
// rousseau-agent's agent loop in your own program. It re-exports the
// stable subset of internal/agent so external modules can import
// this package via
//
//	go get github.com/sebastienrousseau/rousseau-agent/pkg/agent
//
// without touching /internal. The internal package remains the
// source of truth; this façade is a compile-time alias layer.
//
// See examples/embed-agent for a runnable program that uses this
// package.
package agent

import (
	"log/slog"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// Session aliases [agent.Session].
type Session = agent.Session

// Message aliases [agent.Message].
type Message = agent.Message

// Content aliases [agent.Content].
type Content = agent.Content

// Role aliases [agent.Role].
type Role = agent.Role

// Roles.
const (
	RoleUser      = agent.RoleUser
	RoleAssistant = agent.RoleAssistant
	RoleSystem    = agent.RoleSystem
)

// ContentKind aliases [agent.ContentKind].
type ContentKind = agent.ContentKind

// Content kinds.
const (
	ContentText       = agent.ContentText
	ContentImage      = agent.ContentImage
	ContentToolUse    = agent.ContentToolUse
	ContentToolResult = agent.ContentToolResult
)

// Image aliases [agent.Image].
type Image = agent.Image

// Agent aliases [agent.Agent].
type Agent = agent.Agent

// Options aliases [agent.Options].
type Options = agent.Options

// Provider aliases [agent.Provider].
type Provider = agent.Provider

// StreamingProvider aliases [agent.StreamingProvider].
type StreamingProvider = agent.StreamingProvider

// Request aliases [agent.Request].
type Request = agent.Request

// Response aliases [agent.Response].
type Response = agent.Response

// StopReason aliases [agent.StopReason].
type StopReason = agent.StopReason

// Stop reasons.
const (
	StopEndTurn   = agent.StopEndTurn
	StopToolUse   = agent.StopToolUse
	StopMaxTokens = agent.StopMaxTokens
	StopOther     = agent.StopOther
)

// Usage aliases [agent.Usage].
type Usage = agent.Usage

// New constructs a new [Agent]. Thin alias for [agent.New].
func New(p Provider, r *tools.Registry, l *slog.Logger, o Options) *Agent {
	return agent.New(p, r, l, o)
}

// NewSession constructs a [Session]. Alias for [agent.NewSession].
func NewSession(title string) *Session { return agent.NewSession(title) }

// NewUserText constructs a plain-text user Message.
func NewUserText(text string) Message { return agent.NewUserText(text) }

// NewAssistantText constructs a plain-text assistant Message.
func NewAssistantText(text string) Message { return agent.NewAssistantText(text) }

// NewUserImage constructs a plain-image user Message.
func NewUserImage(mediaType string, data []byte, source string) Message {
	return agent.NewUserImage(mediaType, data, source)
}
