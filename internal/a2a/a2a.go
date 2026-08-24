// Package a2a implements the Agent-to-Agent (A2A) protocol so
// rousseau-agent can participate in multi-agent conversations as
// either a peer (server) or a caller (client).
//
// The A2A spec (see [google/A2A]) defines a small JSON-RPC-over-HTTP
// surface for one agent to advertise its capabilities and for another
// to invoke them. This lets a rousseau daemon coordinate with, say, a
// separate spec-writing agent + implementer agent + reviewer agent —
// each running under its own trust boundary but sharing a task
// through a stable protocol.
//
// # Status
//
// **Scaffold only.** The types + interfaces are in place so the
// server/client packages can be built out independently. The wire
// protocol (message envelope, task-artifact schema, streaming
// events) is defined by the upstream spec and is large enough that
// implementing it end-to-end is its own workstream — see
// [`docs/a2a.md`](../../docs/a2a.md) for the design.
//
// [google/A2A]: https://google.github.io/A2A/
package a2a

import "time"

// CapabilityCard is what a rousseau daemon publishes at its A2A
// endpoint to describe what it can do. Kept minimal — the full spec
// has many more optional fields; we'll add them as callers need them.
type CapabilityCard struct {
	// AgentID is the stable identifier a peer uses when referring
	// back to this agent. Typically the daemon's hostname + a
	// deployment-scoped nonce.
	AgentID string `json:"agent_id"`
	// Name is a human-readable label for logs.
	Name string `json:"name"`
	// Version is the rousseau-agent build tag.
	Version string `json:"version"`
	// Skills lists the callable skills / tool bundles the daemon
	// exposes to peers. Each entry maps to an existing
	// [internal/skills] entry when the operator has opted the skill
	// in for A2A exposure.
	Skills []SkillDescriptor `json:"skills,omitempty"`
	// SupportsStreaming indicates whether the agent will emit
	// task/status updates as SSE (true) or only return the final
	// artifact (false).
	SupportsStreaming bool `json:"supports_streaming,omitempty"`
	// PublishedAt is when the card was last rendered.
	PublishedAt time.Time `json:"published_at,omitempty"`
}

// SkillDescriptor is one skill on the CapabilityCard.
type SkillDescriptor struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// Task is the payload one agent sends another when asking it to do
// something. TaskID is expected to be a UUID; the receiving agent
// echoes it back on every update.
type Task struct {
	TaskID string `json:"task_id"`
	// FromAgent is the sender's AgentID.
	FromAgent string `json:"from_agent"`
	// SkillName is the target skill on the receiving agent.
	// Empty means "route to the agent's default handler."
	SkillName string `json:"skill_name,omitempty"`
	// Prompt is the human-readable task description. When SkillName
	// is set, this may be templated per the skill's parameter shape.
	Prompt string `json:"prompt"`
	// InputArtifacts are references to files/blobs the receiving
	// agent will need. TODO: define fetch semantics — inline blob,
	// signed URL, or A2A-native transport?
	InputArtifacts []Artifact `json:"input_artifacts,omitempty"`
}

// TaskStatus is the running / done / error state a receiving agent
// reports back.
type TaskStatus string

// TaskStatus constants.
const (
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

// TaskUpdate is one message on the task's stream. Every update
// echoes the TaskID; agents that don't stream send exactly one
// update with Status=completed / Status=failed.
type TaskUpdate struct {
	TaskID      string     `json:"task_id"`
	Status      TaskStatus `json:"status"`
	Message     string     `json:"message,omitempty"`
	OutputText  string     `json:"output_text,omitempty"`
	Artifacts   []Artifact `json:"artifacts,omitempty"`
	At          time.Time  `json:"at"`
	Progress    float64    `json:"progress,omitempty"` // 0..1
	FailureCode string     `json:"failure_code,omitempty"`
}

// Artifact is a file/blob passed between agents. Uri is either an
// http(s) URL, a data: URI for small inline blobs, or an A2A-native
// artifact:// reference (see docs/a2a.md).
type Artifact struct {
	URI       string `json:"uri"`
	MimeType  string `json:"mime_type,omitempty"`
	Name      string `json:"name,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}
