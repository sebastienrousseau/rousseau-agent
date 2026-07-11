package agent

import (
	"encoding/json"
	"time"
)

// Role identifies the origin of a Message.
type Role string

const (
	// RoleUser marks a message originating from the human operator.
	RoleUser Role = "user"
	// RoleAssistant marks a message originating from the model.
	RoleAssistant Role = "assistant"
	// RoleSystem marks a system-level instruction.
	RoleSystem Role = "system"
)

// ContentKind distinguishes the union variants inside a Content block.
type ContentKind string

const (
	// ContentText is a plain-text block.
	ContentText ContentKind = "text"
	// ContentToolUse is a model-issued tool invocation.
	ContentToolUse ContentKind = "tool_use"
	// ContentToolResult is the outcome of a tool invocation, replayed to
	// the model on the next turn.
	ContentToolResult ContentKind = "tool_result"
)

// Content is a discriminated union carried inside a Message. Exactly one of
// Text, ToolUse, or ToolResult is populated, chosen by Kind.
type Content struct {
	Kind       ContentKind `json:"kind"`
	Text       string      `json:"text,omitempty"`
	ToolUse    *ToolUse    `json:"tool_use,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
}

// ToolUse describes a model-issued tool invocation.
type ToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult carries the outcome of a tool invocation back to the model.
type ToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	Output    string `json:"output"`
	IsError   bool   `json:"is_error,omitempty"`
}

// Message is a single turn in a conversation.
type Message struct {
	Role      Role      `json:"role"`
	Content   []Content `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// NewUserText constructs a plain-text user Message.
func NewUserText(text string) Message {
	return Message{
		Role:      RoleUser,
		Content:   []Content{{Kind: ContentText, Text: text}},
		CreatedAt: time.Now().UTC(),
	}
}

// NewAssistantText constructs a plain-text assistant Message.
func NewAssistantText(text string) Message {
	return Message{
		Role:      RoleAssistant,
		Content:   []Content{{Kind: ContentText, Text: text}},
		CreatedAt: time.Now().UTC(),
	}
}
