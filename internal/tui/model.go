// Package tui implements the Bubble Tea model for the interactive chat.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

// Runner runs a single agent turn. It is the seam between the TUI and
// the agent loop, kept narrow so the TUI never imports llm/anthropic.
type Runner interface {
	Turn(ctx context.Context, s *agent.Session) (agent.Message, error)
}

// StreamingRunner extends Runner with an incremental variant that
// emits agent.StreamEvents into the caller's channel while the turn
// runs. When the concrete Runner satisfies this interface, the TUI
// renders assistant text token-by-token instead of waiting for the
// final Message. Callers own the channel's lifetime up to invocation;
// TurnStream owns closing it before it returns.
type StreamingRunner interface {
	Runner
	TurnStream(ctx context.Context, s *agent.Session, events chan<- agent.StreamEvent) (agent.Message, error)
}

// Model is the top-level Bubble Tea model.
type Model struct {
	runner  Runner
	store   state.Store
	logger  *slog.Logger
	session *agent.Session

	viewport viewport.Model
	textarea textarea.Model
	spinner  spinner.Model

	busy   bool
	width  int
	height int
	err    error

	// streamBuf accumulates text deltas from the current in-flight
	// turn. Rendered under the persisted history and cleared when the
	// turn completes.
	streamBuf strings.Builder
}

// New constructs a Model bound to a Session.
func New(runner Runner, store state.Store, logger *slog.Logger, session *agent.Session) Model {
	ta := textarea.New()
	ta.Placeholder = "Ask, or press Ctrl+C to quit…"
	ta.Focus()
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetHeight(3)

	vp := viewport.New(80, 20)
	vp.SetContent("")

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return Model{
		runner:   runner,
		store:    store,
		logger:   logger,
		session:  session,
		viewport: vp,
		textarea: ta,
		spinner:  sp,
	}
}

// Init satisfies tea.Model.
func (m Model) Init() tea.Cmd { return textarea.Blink }

// turnResult carries the outcome of an agent turn back into the update
// loop.
type turnResult struct {
	msg agent.Message
	err error
}

// Update satisfies tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = maxInt(3, msg.Height-6)
		m.textarea.SetWidth(msg.Width)
		m.viewport.SetContent(renderHistory(m.session, m.width))
		m.viewport.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			if m.busy {
				return m, nil
			}
			text := strings.TrimSpace(m.textarea.Value())
			if text == "" {
				return m, nil
			}
			m.textarea.Reset()
			m.session.Append(agent.NewUserText(text))
			m.busy = true
			m.viewport.SetContent(renderHistory(m.session, m.width))
			m.viewport.GotoBottom()
			return m, tea.Batch(m.spinner.Tick, m.doTurn())
		}

	case streamPumpMsg:
		if msg.delta != "" {
			m.streamBuf.WriteString(msg.delta)
			m.viewport.SetContent(renderHistory(m.session, m.width) + streamPreview(m.streamBuf.String()))
			m.viewport.GotoBottom()
		}
		if msg.next != nil {
			return m, deltaPump(msg.next)
		}
		return m, nil

	case turnResult:
		m.busy = false
		m.streamBuf.Reset()
		if msg.err != nil {
			m.err = msg.err
			m.logger.Error("turn.failed", slog.String("err", msg.err.Error()))
		}
		if err := m.store.Save(context.Background(), m.session); err != nil {
			m.logger.Warn("session.save_failed", slog.String("err", err.Error()))
		}
		m.viewport.SetContent(renderHistory(m.session, m.width))
		m.viewport.GotoBottom()
		return m, nil

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	var vpCmd, taCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	m.textarea, taCmd = m.textarea.Update(msg)
	return m, tea.Batch(vpCmd, taCmd)
}

// View satisfies tea.Model.
func (m Model) View() string {
	header := styleHeader.Render(fmt.Sprintf(" rousseau · %s ", truncate(m.session.Title, 60)))
	status := ""
	if m.busy {
		status = " " + m.spinner.View() + " thinking…"
	} else if m.err != nil {
		status = styleError.Render(" ! " + m.err.Error())
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.viewport.View(),
		m.textarea.View(),
		status,
	)
}

// doTurn returns the tea.Cmd(s) that advance a user turn. When the
// bound Runner also implements StreamingRunner, doTurn kicks off a
// streaming goroutine and returns two commands: one that receives
// streaming messages and another that waits for the final result.
// Otherwise it falls back to the single-shot Turn.
func (m Model) doTurn() tea.Cmd {
	sess := m.session
	if sr, ok := m.runner.(StreamingRunner); ok {
		events := make(chan agent.StreamEvent, 64)
		result := make(chan turnResult, 1)

		go func() {
			msg, err := sr.TurnStream(context.Background(), sess, events)
			result <- turnResult{msg: msg, err: err}
		}()

		// tea.Batch schedules concurrent Cmds. deltaPump repeatedly
		// consumes one event from the channel and re-schedules itself;
		// finalWait blocks on the result channel.
		return tea.Batch(deltaPump(events), finalWait(result))
	}
	runner := m.runner
	return func() tea.Msg {
		msg, err := runner.Turn(context.Background(), sess)
		return turnResult{msg: msg, err: err}
	}
}

// deltaPump receives a single event from events and returns an
// appropriate tea.Msg. Bubble Tea will re-schedule the Cmd if we
// return a follow-up Cmd via a batch — we implement that by making
// the Msg carry a Cmd back to Update.
func deltaPump(events <-chan agent.StreamEvent) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-events
		if !ok {
			return nil
		}
		if evt.Kind == agent.StreamTextDelta && evt.Delta != "" {
			return streamPumpMsg{delta: evt.Delta, next: events}
		}
		// Non-text event — keep pumping.
		return streamPumpMsg{next: events}
	}
}

// streamPumpMsg carries an optional delta plus the channel we should
// keep pumping. Update consumes the delta and re-schedules deltaPump.
type streamPumpMsg struct {
	delta string
	next  <-chan agent.StreamEvent
}

func finalWait(result <-chan turnResult) tea.Cmd {
	return func() tea.Msg {
		return <-result
	}
}

// streamPreview renders the in-flight assistant text under the
// persisted history so the user sees tokens as they arrive.
func streamPreview(text string) string {
	if text == "" {
		return ""
	}
	return "\n" + styleAgent.Render("rousseau") + "\n" + text + "\n"
}

var (
	styleHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F5A623")).Padding(0, 1)
	styleUser   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8AB4F8"))
	styleAgent  = lipgloss.NewStyle().Foreground(lipgloss.Color("#C5E1A5"))
	styleTool   = lipgloss.NewStyle().Foreground(lipgloss.Color("#B39DDB")).Italic(true)
	styleError  = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF9A9A")).Bold(true)
)

func renderHistory(s *agent.Session, width int) string {
	_ = width
	var b strings.Builder
	for _, m := range s.Messages {
		switch m.Role {
		case agent.RoleUser:
			b.WriteString(styleUser.Render("you"))
			b.WriteString("\n")
		case agent.RoleAssistant:
			b.WriteString(styleAgent.Render("rousseau"))
			b.WriteString("\n")
		default:
			continue
		}
		for _, c := range m.Content {
			switch c.Kind {
			case agent.ContentText:
				b.WriteString(c.Text)
				b.WriteString("\n")
			case agent.ContentToolUse:
				if c.ToolUse != nil {
					b.WriteString(styleTool.Render(fmt.Sprintf("→ %s(%s)", c.ToolUse.Name, string(c.ToolUse.Input))))
					b.WriteString("\n")
				}
			case agent.ContentToolResult:
				if c.ToolResult != nil {
					prefix := "←"
					if c.ToolResult.IsError {
						prefix = "×"
					}
					b.WriteString(styleTool.Render(fmt.Sprintf("%s %s", prefix, truncate(c.ToolResult.Output, 400))))
					b.WriteString("\n")
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
