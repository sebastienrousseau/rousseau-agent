package a2a

import (
	"encoding/json"
	"fmt"
	"time"
)

// SpecVersion is the A2A wire-protocol version this package targets
// on its v1-shaped routes. Emitted in the `A2A-Version` response
// header on every spec route.
const SpecVersion = "1.0"

// ContentTypeSpec is the preferred Content-Type for the REST binding
// as of A2A v1.0.1. Legacy `application/json` is still accepted for
// backwards compatibility but new server routes emit this value.
const ContentTypeSpec = "application/a2a+json"

// TaskState is the v1.0 ProtoJSON-shaped lifecycle enum. Spec
// reference: `specification/a2a.proto` `enum TaskState`.
type TaskState string

// TaskState constants — the ProtoJSON string form used on the wire.
// The v1.0 spec (ADR-001) aligned enum values to ProtoJSON, and PR
// #1283 canonicalised American spelling ("canceled", not "cancelled").
const (
	TaskStateUnspecified   TaskState = "TASK_STATE_UNSPECIFIED"
	TaskStateSubmitted     TaskState = "TASK_STATE_SUBMITTED"
	TaskStateWorking       TaskState = "TASK_STATE_WORKING"
	TaskStateInputRequired TaskState = "TASK_STATE_INPUT_REQUIRED"
	TaskStateAuthRequired  TaskState = "TASK_STATE_AUTH_REQUIRED"
	TaskStateCompleted     TaskState = "TASK_STATE_COMPLETED"
	TaskStateFailed        TaskState = "TASK_STATE_FAILED"
	TaskStateCanceled      TaskState = "TASK_STATE_CANCELED"
	TaskStateRejected      TaskState = "TASK_STATE_REJECTED"
)

// IsTerminal reports whether the state is one from which no further
// updates can arrive.
func (s TaskState) IsTerminal() bool {
	switch s {
	case TaskStateCompleted, TaskStateFailed, TaskStateCanceled, TaskStateRejected:
		return true
	default:
		return false
	}
}

// FromLegacyStatus converts a v0-legacy [TaskStatus] into its v1.0
// equivalent. Unknown values map to [TaskStateUnspecified].
func FromLegacyStatus(s TaskStatus) TaskState {
	switch s {
	case TaskStatusRunning:
		return TaskStateWorking
	case TaskStatusCompleted:
		return TaskStateCompleted
	case TaskStatusFailed:
		return TaskStateFailed
	case TaskStatusCancelled:
		return TaskStateCanceled
	default:
		return TaskStateUnspecified
	}
}

// ToLegacyStatus is the inverse of [FromLegacyStatus] — collapses the
// finer-grained v1 lifecycle into the v0 four-state enum. Non-terminal
// v1 states other than working (submitted, input_required,
// auth_required) collapse to running because v0 does not distinguish
// them; rejected collapses to failed.
func (s TaskState) ToLegacyStatus() TaskStatus {
	switch s {
	case TaskStateSubmitted, TaskStateWorking, TaskStateInputRequired, TaskStateAuthRequired:
		return TaskStatusRunning
	case TaskStateCompleted:
		return TaskStatusCompleted
	case TaskStateFailed, TaskStateRejected:
		return TaskStatusFailed
	case TaskStateCanceled:
		return TaskStatusCancelled
	default:
		return TaskStatusRunning
	}
}

// Role enumerates the message speaker per the A2A spec.
type Role string

// Role constants.
const (
	RoleUser  Role = "user"
	RoleAgent Role = "agent"
)

// PartKind is the tag on a polymorphic [Part]. Spec enum values are
// lowercase (per `oneof part` in a2a.proto).
type PartKind string

// PartKind constants.
const (
	PartKindText PartKind = "text"
	PartKindFile PartKind = "file"
	PartKindData PartKind = "data"
)

// Part is one element in [Message.Parts] or [SpecArtifact.Parts]. It is
// a discriminated union tagged by [Part.Kind]:
//
//   - kind=text  → [Text] is the payload
//   - kind=file  → [File] carries a [FilePart]
//   - kind=data  → [Data] carries an arbitrary JSON value
//
// The other fields are ignored / omitted for kinds that don't use them.
// Marshaling emits only the kind + its own payload so peers see a
// clean discriminated shape.
type Part struct {
	Kind PartKind        `json:"kind"`
	Text string          `json:"text,omitempty"`
	File *FilePart       `json:"file,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
	// Metadata is an optional arbitrary bag per the spec.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// FilePart carries a file reference. Exactly one of Bytes / URI must
// be set; the spec permits both but interop is cleanest when clients
// pick one.
type FilePart struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Bytes    string `json:"bytes,omitempty"` // base64
	URI      string `json:"uri,omitempty"`
}

// Message is the v1.0 spec message envelope. Callers hitting
// `POST /message:send` submit one of these; agents emit them on the
// streaming path.
type Message struct {
	MessageID        string         `json:"messageId"`
	ContextID        string         `json:"contextId,omitempty"`
	TaskID           string         `json:"taskId,omitempty"`
	Role             Role           `json:"role"`
	Parts            []Part         `json:"parts"`
	ReferenceTaskIDs []string       `json:"referenceTaskIds,omitempty"`
	Extensions       []string       `json:"extensions,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

// SpecArtifact is the v1.0 spec artifact shape. Legacy [Artifact] on
// the same package is preserved for backward compatibility; new code
// should target this shape.
type SpecArtifact struct {
	ArtifactID  string         `json:"artifactId"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []Part         `json:"parts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Extensions  []string       `json:"extensions,omitempty"`
}

// TaskStatus1 is a v1.0-shaped task status carrier. Not to be
// confused with the legacy string-typed [TaskStatus] on the same
// package (kept named without a version suffix to preserve the v0.0.2
// API). The suffix "1" marks the v1.0 shape.
type TaskStatus1 struct {
	State     TaskState `json:"state"`
	Message   *Message  `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// SpecTask is the v1.0 spec task envelope returned from
// `POST /message:send` and `GET /tasks/{id}`.
type SpecTask struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId,omitempty"`
	Status    TaskStatus1    `json:"status"`
	Artifacts []SpecArtifact `json:"artifacts,omitempty"`
	History   []Message      `json:"history,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// TaskStatusUpdateEvent is one event on the `POST /message:stream` or
// `GET /tasks/{id}:subscribe` SSE stream. v1.0 removed the `final`
// field from this event — terminal state is derived from [Status.State]
// via [TaskState.IsTerminal].
type TaskStatusUpdateEvent struct {
	TaskID    string      `json:"taskId"`
	ContextID string      `json:"contextId,omitempty"`
	Kind      string      `json:"kind"` // always "status-update"
	Status    TaskStatus1 `json:"status"`
}

// TaskArtifactUpdateEvent is the artifact variant of the streaming
// event union.
type TaskArtifactUpdateEvent struct {
	TaskID    string       `json:"taskId"`
	ContextID string       `json:"contextId,omitempty"`
	Kind      string       `json:"kind"` // always "artifact-update"
	Artifact  SpecArtifact `json:"artifact"`
	Append    bool         `json:"append,omitempty"`
	LastChunk bool         `json:"lastChunk,omitempty"`
}

// StreamResponse is the SSE `data:` payload — a oneof carrying exactly
// one of the enclosed shapes. Marshaling omits nil / zero fields so
// peers see the same discriminated shape the spec's protobuf oneof
// produces.
type StreamResponse struct {
	Task           *SpecTask                `json:"task,omitempty"`
	Message        *Message                 `json:"message,omitempty"`
	StatusUpdate   *TaskStatusUpdateEvent   `json:"statusUpdate,omitempty"`
	ArtifactUpdate *TaskArtifactUpdateEvent `json:"artifactUpdate,omitempty"`
}

// AgentCard is the v1.0-shaped card served at
// `/.well-known/agent-card.json`. Only the fields rousseau
// currently populates are declared as required — the spec's
// optional-heavy shape is honoured via omitempty.
type AgentCard struct {
	Name               string                `json:"name"`
	Description        string                `json:"description,omitempty"`
	Version            string                `json:"version"`
	Provider           *AgentProvider        `json:"provider,omitempty"`
	URL                string                `json:"url,omitempty"`
	Interfaces         []AgentInterface      `json:"interfaces,omitempty"`
	Capabilities       AgentCapabilities     `json:"capabilities"`
	SecuritySchemes    map[string]any        `json:"securitySchemes,omitempty"`
	Security           []map[string][]string `json:"security,omitempty"`
	DefaultInputModes  []string              `json:"defaultInputModes,omitempty"`
	DefaultOutputModes []string              `json:"defaultOutputModes,omitempty"`
	Skills             []AgentSkill          `json:"skills,omitempty"`
	Signatures         []any                 `json:"signatures,omitempty"`
	IconURL            string                `json:"iconUrl,omitempty"`
	DocumentationURL   string                `json:"documentationUrl,omitempty"`
	// PreferredTransport communicates which of interfaces[] the peer
	// should prefer. Spec-optional; present for parity with the
	// reference implementations.
	PreferredTransport string `json:"preferredTransport,omitempty"`
	// ProtocolVersion is the A2A version the card publisher speaks.
	ProtocolVersion string `json:"protocolVersion,omitempty"`
}

// AgentProvider is the "who publishes this agent" descriptor.
type AgentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url,omitempty"`
}

// AgentInterface advertises a single transport binding on the card.
// Multiple entries mean the peer supports multiple transports; the
// client picks.
type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"` // "REST" | "JSONRPC" | "GRPC"
	Tenant          string `json:"tenant,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
}

// AgentCapabilities is the boolean-and-list flag bag on the card.
type AgentCapabilities struct {
	Streaming              bool     `json:"streaming,omitempty"`
	PushNotifications      bool     `json:"pushNotifications,omitempty"`
	ExtendedAgentCard      bool     `json:"extendedAgentCard,omitempty"`
	StateTransitionHistory bool     `json:"stateTransitionHistory,omitempty"`
	Extensions             []string `json:"extensions,omitempty"`
}

// AgentSkill is one skill entry on the card. The v1.0 shape is much
// richer than the legacy [SkillDescriptor]; both live in the package
// so old callers keep compiling.
type AgentSkill struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Examples    []string              `json:"examples,omitempty"`
	InputModes  []string              `json:"inputModes,omitempty"`
	OutputModes []string              `json:"outputModes,omitempty"`
	Security    []map[string][]string `json:"security,omitempty"`
}

// UpgradeCard renders a v1.0 [AgentCard] from the v0-shorthand
// [CapabilityCard]. Optional fields (provider, iconUrl, etc.) stay
// zero — populate them on the returned card if the operator has
// configured them.
func UpgradeCard(legacy CapabilityCard) AgentCard {
	skills := make([]AgentSkill, 0, len(legacy.Skills))
	for _, s := range legacy.Skills {
		skills = append(skills, AgentSkill{
			ID:          s.Name,
			Name:        s.Name,
			Description: s.Description,
			Tags:        s.Tags,
		})
	}
	return AgentCard{
		Name:            legacy.Name,
		Version:         legacy.Version,
		Skills:          skills,
		ProtocolVersion: SpecVersion,
		Capabilities: AgentCapabilities{
			Streaming: legacy.SupportsStreaming,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
	}
}

// TextMessage constructs a role=user single-text-part [Message] with a
// generated messageId. Handy for callers that just want the "here is
// my prompt" shape without hand-rolling the union.
func TextMessage(prompt string) Message {
	return Message{
		MessageID: newMessageID(),
		Role:      RoleUser,
		Parts:     []Part{{Kind: PartKindText, Text: prompt}},
	}
}

// PromptFromMessage extracts the concatenated text parts of a
// [Message] so a v1-shaped submission can be handed to a legacy
// Handler whose Task type expects a flat prompt string.
func PromptFromMessage(m Message) string {
	if len(m.Parts) == 0 {
		return ""
	}
	if len(m.Parts) == 1 && m.Parts[0].Kind == PartKindText {
		return m.Parts[0].Text
	}
	out := ""
	for i, p := range m.Parts {
		if p.Kind != PartKindText {
			continue
		}
		if i > 0 && out != "" {
			out += "\n"
		}
		out += p.Text
	}
	return out
}

// newMessageID mints a spec-conformant message id.
var newMessageID = func() string {
	return fmt.Sprintf("msg-%d", time.Now().UnixNano())
}
