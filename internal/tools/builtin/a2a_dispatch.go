package builtin

// The a2a_dispatch tool lets the agent hand a task off to a
// configured peer A2A agent. The peer's SubmitTask response (a
// stream of TaskUpdate frames) is collected until the terminal
// status arrives; the concatenation of every OutputText from the
// stream is returned to the model.
//
// Design decisions worth noting:
//
//   - Peer allow-list is fixed at construction. The tool refuses
//     unknown peers so a hallucinated peer name is a clean 400,
//     not a silent network call.
//   - Blocking-until-terminal is the simplest correct semantic for
//     a tool-call surface (the model expects one output per call).
//   - Live progress emission: each non-terminal peer update fires a
//     progress.Event onto the caller's progress.Bus (looked up from
//     ctx). Chat transports render these as "peer is still working
//     on it…" bubbles so users see life during long dispatches.
//     When no publisher is on the context (headless usage, tests,
//     embedded), emission drops silently — no behaviour change vs
//     the non-streaming implementation.
//   - No approver bypass. This tool is a regular tool from the
//     registry's perspective — the daemon's Approver still gates
//     every call.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
	a2aclient "github.com/sebastienrousseau/rousseau-agent/internal/a2a/client"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// A2ADispatchTool dispatches a task to a configured A2A peer and
// returns the peer's response.
type A2ADispatchTool struct {
	peers map[string]*a2aclient.Client
	// DefaultTimeout bounds a single dispatch when the tool arg
	// omits timeout_seconds. Zero uses 120s — long enough that
	// most peer conversations complete but short enough that a
	// wedged peer doesn't wedge the agent.
	DefaultTimeout time.Duration
}

// NewA2ADispatchTool constructs the tool. Callers pass the
// name→client map assembled at daemon boot. A nil or empty map is
// legal — the tool constructs but every Execute returns an error
// telling the model no peers are configured. This keeps the tool's
// existence a policy decision (register or not) separate from its
// runtime behaviour.
func NewA2ADispatchTool(peers map[string]*a2aclient.Client) *A2ADispatchTool {
	return &A2ADispatchTool{peers: peers, DefaultTimeout: 120 * time.Second}
}

// Name returns the tool identifier the model sees.
func (*A2ADispatchTool) Name() string { return "a2a_dispatch" }

// Description is what the model reads to decide whether to use the
// tool. Kept concrete about the input shape + return semantics so
// the model doesn't have to guess.
func (t *A2ADispatchTool) Description() string {
	names := t.peerNames()
	if len(names) == 0 {
		return "Dispatch a task to a peer A2A agent. NO PEERS ARE CONFIGURED — this tool will return an error."
	}
	return fmt.Sprintf(
		"Dispatch a task to a peer A2A agent. Available peers: %s. "+
			"The peer's response text is returned as the tool output. "+
			"Blocks until the peer's task reaches a terminal status. "+
			"Intermediate progress from the peer streams into the user's "+
			"chat transport as live status updates.",
		strings.Join(names, ", "),
	)
}

// InputSchema is the model-facing JSON schema. The `peer` enum is
// populated dynamically from the peer map so the model sees the
// same allow-list the executor enforces.
func (t *A2ADispatchTool) InputSchema() map[string]any {
	names := t.peerNames()
	peerProp := map[string]any{
		"type":        "string",
		"description": "The configured peer name to dispatch to.",
	}
	if len(names) > 0 {
		peerProp["enum"] = names
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"peer":   peerProp,
			"prompt": map[string]any{"type": "string", "description": "The prompt to send to the peer agent."},
			"skill_name": map[string]any{
				"type":        "string",
				"description": "Optional: name a skill on the peer to invoke directly. Empty routes to the peer's default handler.",
			},
			"timeout_seconds": map[string]any{
				"type":        "number",
				"description": "Optional per-call timeout in seconds. Default 120.",
			},
		},
		"required": []string{"peer", "prompt"},
	}
}

type a2aDispatchInput struct {
	Peer           string  `json:"peer"`
	Prompt         string  `json:"prompt"`
	SkillName      string  `json:"skill_name,omitempty"`
	TimeoutSeconds float64 `json:"timeout_seconds,omitempty"`
}

// Execute performs the peer call.
func (t *A2ADispatchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in a2aDispatchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("a2a_dispatch: parse input: %w", err)
	}
	if in.Peer == "" {
		return "", errors.New("a2a_dispatch: peer is required")
	}
	if in.Prompt == "" && in.SkillName == "" {
		return "", errors.New("a2a_dispatch: prompt or skill_name is required")
	}
	client, ok := t.peers[in.Peer]
	if !ok {
		return "", fmt.Errorf("a2a_dispatch: unknown peer %q; configured peers: %s", in.Peer, strings.Join(t.peerNames(), ", "))
	}

	timeout := t.DefaultTimeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds * float64(time.Second))
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ch, err := client.SubmitTask(dispatchCtx, a2a.Task{
		FromAgent: "rousseau-agent",
		SkillName: in.SkillName,
		Prompt:    in.Prompt,
	})
	if err != nil {
		return "", fmt.Errorf("a2a_dispatch: submit to %q: %w", in.Peer, err)
	}

	// Progress publisher for live "peer is working…" updates. When
	// no publisher is on ctx (headless, tests) this is Nop and every
	// call is a no-op — matching the pre-streaming behaviour.
	pub := progress.PublisherFrom(ctx)
	if pub == nil {
		pub = progress.Nop{}
	}
	emitProgress := func(upd a2a.TaskUpdate) {
		text := upd.Message
		if text == "" {
			text = upd.OutputText
		}
		// Skip empty-text updates — the transport would render an
		// empty bubble, which is worse than no update at all.
		if text == "" {
			return
		}
		progress.Emit(ctx, pub, progress.Event{
			Kind:   progress.KindToolStarted,
			Tool:   t.Name() + "/" + in.Peer,
			Text:   text,
			Detail: string(upd.Status),
		})
	}

	var (
		outputs     []string
		lastStatus  a2a.TaskStatus
		failureMsg  string
		failureCode string
	)
	for upd := range ch {
		lastStatus = upd.Status
		if upd.OutputText != "" {
			outputs = append(outputs, upd.OutputText)
		}
		if upd.Status == a2a.TaskStatusFailed {
			failureMsg = upd.Message
			failureCode = upd.FailureCode
		}
		// Only emit progress for NON-terminal frames. The terminal
		// frame's contents ride the tool's return value, which the
		// transport reporter already surfaces via KindToolFinished
		// from the outer tool loop — a duplicate here would show up
		// as two bubbles for the same terminal event.
		if !isA2ATerminal(upd.Status) {
			emitProgress(upd)
		}
	}
	switch lastStatus {
	case a2a.TaskStatusCompleted:
		if len(outputs) == 0 {
			return "(peer completed with no output)", nil
		}
		return strings.Join(outputs, "\n"), nil
	case a2a.TaskStatusFailed:
		return "", fmt.Errorf("a2a_dispatch: peer %q reported failure (%s): %s", in.Peer, failureCode, failureMsg)
	case a2a.TaskStatusCancelled:
		return "", fmt.Errorf("a2a_dispatch: peer %q cancelled the task", in.Peer)
	default:
		// Non-terminal last status means the stream ended without
		// a final frame — either a network hiccup or a peer that
		// hung up. Surface concretely so the model doesn't retry
		// into the same hole.
		return "", fmt.Errorf("a2a_dispatch: peer %q stream ended without a terminal status (last=%s)", in.Peer, lastStatus)
	}
}

// isA2ATerminal reports whether s is a terminal task status. Kept
// local because the a2a package's own terminal set is scoped to
// spec-shaped states (TaskState); this tool works with the legacy
// TaskStatus enum that SubmitTask still returns.
func isA2ATerminal(s a2a.TaskStatus) bool {
	switch s {
	case a2a.TaskStatusCompleted, a2a.TaskStatusFailed, a2a.TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// peerNames returns a sorted slice of configured peer names.
// Sorted so the InputSchema enum + description are deterministic —
// callers reading the tool definition twice see identical output.
func (t *A2ADispatchTool) peerNames() []string {
	if len(t.peers) == 0 {
		return nil
	}
	out := make([]string, 0, len(t.peers))
	for name := range t.peers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
