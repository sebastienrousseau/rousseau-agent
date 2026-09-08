package cli

// A2A server-side task handler — bridges an inbound [a2a.Task] to an
// [agent.Turn] call on a fresh per-task session.
//
// Session isolation: every inbound task gets its own [agent.Session]
// so a hostile peer can't observe or influence other conversations.
// The session's Sender is set to "a2a/<peer-agent>" so the identity
// resolver + audit trail see A2A tasks as first-class actors.
//
// Skill exposure: [A2AServerConfig.ExposedSkills] is an allow-list —
// tasks that name a SkillName outside the allow-list are rejected
// with Status=failed. Tasks with no SkillName invoke the agent's
// default handler with the Task.Prompt as the user turn.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// a2aTaskHandler satisfies a2a/server.Handler by running each task
// as a fresh agent.Turn on a bespoke session.
type a2aTaskHandler struct {
	agent         *agent.Agent
	exposedSkills map[string]struct{} // name → {}
	logger        *slog.Logger
}

// newA2ATaskHandler constructs the handler. exposedSkills is an
// allow-list; empty means "no skills exposed" (bare prompt only).
func newA2ATaskHandler(ag *agent.Agent, exposedSkills []string, logger *slog.Logger) *a2aTaskHandler {
	m := make(map[string]struct{}, len(exposedSkills))
	for _, s := range exposedSkills {
		m[s] = struct{}{}
	}
	return &a2aTaskHandler{agent: ag, exposedSkills: m, logger: logger}
}

// OnTask satisfies a2a/server.Handler. Runs one turn per inbound task.
func (h *a2aTaskHandler) OnTask(ctx context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
	if err := h.validateTask(task); err != nil {
		emit(a2a.TaskUpdate{
			Status:      a2a.TaskStatusFailed,
			Message:     err.Error(),
			FailureCode: "invalid_task",
		})
		// Return nil rather than the validation error: a2a/server
		// synthesises its OWN Status=failed update when OnTask
		// returns non-nil, which would duplicate the emit we just
		// sent and produce two terminal frames on the SSE stream.
		// Peers unmarshalling a second frame after terminal are a
		// class of bug we specifically want to prevent.
		return nil //nolint:nilerr // deliberate: we've already emitted the failure; returning err would double-emit
	}

	sess := h.newSession(task)
	emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "session started"})

	msg, err := h.agent.Turn(ctx, sess)
	if err != nil {
		h.logger.Warn("a2a.turn_failed",
			slog.String("task_id", task.TaskID),
			slog.String("from_agent", task.FromAgent),
			slog.String("err", err.Error()),
		)
		emit(a2a.TaskUpdate{
			Status:      a2a.TaskStatusFailed,
			Message:     err.Error(),
			FailureCode: "turn_error",
		})
		// Same rationale as the validate path: we've emitted the
		// terminal update; returning the error would let a2a/server
		// double-emit its synthesised failure frame.
		return nil
	}

	emit(a2a.TaskUpdate{
		Status:     a2a.TaskStatusCompleted,
		OutputText: extractAssistantText(msg),
	})
	return nil
}

// validateTask enforces the two operator-facing invariants:
//  1. Non-empty prompt or a whitelisted SkillName.
//  2. Named skills must be in the exposed allow-list.
func (h *a2aTaskHandler) validateTask(task a2a.Task) error {
	if task.Prompt == "" && task.SkillName == "" {
		return fmt.Errorf("task must set prompt or skill_name")
	}
	if task.SkillName != "" {
		if _, ok := h.exposedSkills[task.SkillName]; !ok {
			return fmt.Errorf("skill %q is not exposed for A2A invocation", task.SkillName)
		}
	}
	return nil
}

// newSession builds the per-task Session. Title uses the peer's
// AgentID so operators reviewing sessions can find A2A ones quickly.
// Sender is "a2a/<peer>" so the identity resolver + audit trail see
// A2A traffic as a distinct actor class.
func (h *a2aTaskHandler) newSession(task a2a.Task) *agent.Session {
	title := "A2A task from " + task.FromAgent
	if task.SkillName != "" {
		title += " (" + task.SkillName + ")"
	}
	sess := agent.NewSession(title)
	sess.Sender = "a2a/" + task.FromAgent
	sess.Append(agent.NewUserText(a2aPromptFor(task)))
	return sess
}

// a2aPromptFor composes the user-visible prompt from a Task. Skill
// invocations wrap the operator's prompt in a directive that names
// the intended skill so the agent picks it up via the same
// discovery path chat transports use.
func a2aPromptFor(task a2a.Task) string {
	if task.SkillName == "" {
		return task.Prompt
	}
	if task.Prompt == "" {
		return "Invoke the " + task.SkillName + " skill."
	}
	return "Invoke the " + task.SkillName + " skill. Details: " + task.Prompt
}

// extractAssistantText concatenates every text block on an agent
// [agent.Message] into a single string suitable for TaskUpdate.OutputText.
// Non-text blocks (tool-use, thinking) are skipped.
func extractAssistantText(msg agent.Message) string {
	var out strings.Builder
	for _, c := range msg.Content {
		if c.Kind != agent.ContentText {
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(c.Text)
	}
	return out.String()
}
