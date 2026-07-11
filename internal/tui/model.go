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

	case turnResult:
		m.busy = false
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

func (m Model) doTurn() tea.Cmd {
	sess := m.session
	runner := m.runner
	return func() tea.Msg {
		msg, err := runner.Turn(context.Background(), sess)
		return turnResult{msg: msg, err: err}
	}
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
