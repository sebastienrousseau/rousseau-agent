package agent

import "github.com/sebastienrousseau/rousseau-agent/internal/model"

// The conversation domain types live in internal/model so providers
// and stores can be built without the agent loop's dependencies. They
// are re-exported here as aliases, which are the same types, not
// copies: an agent.Message is a model.Message and may be passed to
// either package's functions.

// Role aliases [model.Role].
type Role = model.Role

// Role values.
const (
	RoleUser      = model.RoleUser
	RoleAssistant = model.RoleAssistant
	RoleSystem    = model.RoleSystem
)

// ContentKind aliases [model.ContentKind].
type ContentKind = model.ContentKind

// ContentKind values.
const (
	ContentText       = model.ContentText
	ContentImage      = model.ContentImage
	ContentToolUse    = model.ContentToolUse
	ContentToolResult = model.ContentToolResult
)

// Content aliases [model.Content].
type Content = model.Content

// Image aliases [model.Image].
type Image = model.Image

// ToolUse aliases [model.ToolUse].
type ToolUse = model.ToolUse

// ToolResult aliases [model.ToolResult].
type ToolResult = model.ToolResult

// Message aliases [model.Message].
type Message = model.Message

// NewUserText aliases [model.NewUserText].
func NewUserText(text string) Message { return model.NewUserText(text) }

// NewAssistantText aliases [model.NewAssistantText].
func NewAssistantText(text string) Message { return model.NewAssistantText(text) }

// NewUserImage aliases [model.NewUserImage].
func NewUserImage(mediaType string, data []byte, source string) Message {
	return model.NewUserImage(mediaType, data, source)
}

// Session aliases [model.Session].
type Session = model.Session

// NewSession aliases [model.NewSession].
func NewSession(title string) *Session { return model.NewSession(title) }

// Request aliases [model.Request].
type Request = model.Request

// Response aliases [model.Response].
type Response = model.Response

// Usage aliases [model.Usage].
type Usage = model.Usage

// StopReason aliases [model.StopReason].
type StopReason = model.StopReason

// StopReason values.
const (
	StopEndTurn   = model.StopEndTurn
	StopToolUse   = model.StopToolUse
	StopMaxTokens = model.StopMaxTokens
	StopOther     = model.StopOther
)

// Provider aliases [model.Provider].
type Provider = model.Provider

// StreamingProvider aliases [model.StreamingProvider].
type StreamingProvider = model.StreamingProvider

// StreamEvent aliases [model.StreamEvent].
type StreamEvent = model.StreamEvent

// StreamEventKind aliases [model.StreamEventKind].
type StreamEventKind = model.StreamEventKind

// StreamEventKind values.
const (
	StreamStart     = model.StreamStart
	StreamTextDelta = model.StreamTextDelta
	StreamToolUse   = model.StreamToolUse
	StreamResult    = model.StreamResult
	StreamOther     = model.StreamOther
)

// StreamReport aliases [model.StreamReport].
type StreamReport = model.StreamReport
