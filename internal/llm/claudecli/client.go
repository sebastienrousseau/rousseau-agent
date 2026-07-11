// Package claudecli implements agent.Provider by shelling out to the
// installed `claude` CLI (Claude Code). It uses whatever authentication
// the user's Claude Code install already has — subscription, API key
// helper, keychain — so callers do not need to plumb ANTHROPIC_API_KEY.
//
// Because `claude` runs its own tool-use loop internally, requests
// through this provider execute tools inside the claude subprocess.
// The agent-level tools registered on the Registry are NOT invoked for
// this provider; the Response is always a single end-of-turn text
// message with claude's final answer.
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Config configures the Provider.
type Config struct {
	// Binary is the claude executable to invoke. Empty defaults to
	// "claude" (resolved on $PATH).
	Binary string
	// Model is passed to --model. Empty uses claude's default.
	Model string
	// PermissionMode is passed to --permission-mode. Empty uses
	// claude's default. For unattended daemons (e.g. WhatsApp bridge)
	// "bypassPermissions" is required, but you accept the blast radius.
	PermissionMode string
	// ExtraArgs are prepended before -p on every invocation. Useful for
	// --add-dir, --allowed-tools, --disallowed-tools, --plugin-dir …
	ExtraArgs []string
}

// runFunc executes an exec.Cmd; extracted so tests can stub it.
type runFunc func(cmd *exec.Cmd) ([]byte, error)

// Provider is an agent.Provider backed by the `claude` CLI.
type Provider struct {
	cfg Config
	run runFunc
}

// New constructs a Provider. It does not verify the binary exists;
// invocations that fail surface at Complete time.
func New(cfg Config) *Provider {
	if cfg.Binary == "" {
		cfg.Binary = "claude"
	}
	return &Provider{cfg: cfg, run: defaultRun}
}

// Name returns the provider identifier.
func (*Provider) Name() string { return "claudecli" }

// Complete runs a one-shot claude invocation. The full message history
// is NOT sent — claude maintains conversation state via --session-id.
// The last user message is used as the prompt; system prompt is passed
// via --system-prompt on the first turn only (claude ignores it on
// resumes, which is what we want).
func (p *Provider) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	prompt, err := lastUserText(req.Messages)
	if err != nil {
		return agent.Response{}, err
	}

	args := []string{"--print", "--output-format", "json"}
	if req.SessionID != "" {
		args = append(args, "--session-id", req.SessionID)
	}
	if req.System != "" {
		args = append(args, "--system-prompt", req.System)
	}
	if p.cfg.Model != "" {
		args = append(args, "--model", p.cfg.Model)
	}
	if p.cfg.PermissionMode != "" {
		args = append(args, "--permission-mode", p.cfg.PermissionMode)
	}
	args = append(args, p.cfg.ExtraArgs...)
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, p.cfg.Binary, args...)
	out, err := p.run(cmd)
	if err != nil {
		return agent.Response{}, fmt.Errorf("claudecli: run: %w: %s", err, truncate(string(out), 400))
	}

	return parseResult(out)
}

// cliResult is the subset of `claude -p --output-format json`'s output
// that we care about.
type cliResult struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	APIErrorStatus json.RawMessage `json:"api_error_status"`
}

func parseResult(raw []byte) (agent.Response, error) {
	// `claude -p` sometimes emits a leading log line before the JSON.
	// Locate the first '{' and parse from there.
	i := bytes.IndexByte(raw, '{')
	if i < 0 {
		return agent.Response{}, fmt.Errorf("claudecli: no JSON in output: %s", truncate(string(raw), 200))
	}
	var res cliResult
	if err := json.Unmarshal(raw[i:], &res); err != nil {
		return agent.Response{}, fmt.Errorf("claudecli: parse JSON: %w", err)
	}
	if res.IsError {
		msg := res.Result
		if msg == "" {
			msg = string(res.APIErrorStatus)
		}
		return agent.Response{}, fmt.Errorf("claudecli: model error: %s", truncate(msg, 400))
	}
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: res.Result}},
		},
		StopReason: mapStop(res.StopReason),
		Usage: agent.Usage{
			InputTokens:  res.Usage.InputTokens,
			OutputTokens: res.Usage.OutputTokens,
		},
	}, nil
}

func mapStop(s string) agent.StopReason {
	switch s {
	case "end_turn":
		return agent.StopEndTurn
	case "max_tokens":
		return agent.StopMaxTokens
	default:
		return agent.StopEndTurn
	}
}

func lastUserText(msgs []agent.Message) (string, error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != agent.RoleUser {
			continue
		}
		var b strings.Builder
		for _, c := range msgs[i].Content {
			if c.Kind == agent.ContentText {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(c.Text)
			}
		}
		if b.Len() == 0 {
			continue
		}
		return b.String(), nil
	}
	return "", errors.New("claudecli: no user text message to send")
}

func defaultRun(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
