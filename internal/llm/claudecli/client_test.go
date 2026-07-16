package claudecli

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func TestNew_DefaultsBinary(t *testing.T) {
	p := New(Config{})
	assert.Equal(t, "claude", p.cfg.Binary)
	assert.Equal(t, "claudecli", p.Name())
}

func TestNew_KeepsExplicitBinary(t *testing.T) {
	p := New(Config{Binary: "/opt/claude"})
	assert.Equal(t, "/opt/claude", p.cfg.Binary)
}

func TestMapStop(t *testing.T) {
	assert.Equal(t, agent.StopEndTurn, mapStop("end_turn"))
	assert.Equal(t, agent.StopMaxTokens, mapStop("max_tokens"))
	assert.Equal(t, agent.StopEndTurn, mapStop("unknown"))
}

func TestLastUserContent_Basic(t *testing.T) {
	msgs := []agent.Message{
		agent.NewAssistantText("earlier"),
		agent.NewUserText("hello world"),
	}
	got, _, err := lastUserContent(msgs)
	require.NoError(t, err)
	assert.Equal(t, "hello world", got)
}

func TestLastUserContent_ConcatenatesTextBlocks(t *testing.T) {
	msgs := []agent.Message{{
		Role: agent.RoleUser,
		Content: []agent.Content{
			{Kind: agent.ContentText, Text: "a"},
			{Kind: agent.ContentText, Text: "b"},
		},
	}}
	got, _, err := lastUserContent(msgs)
	require.NoError(t, err)
	assert.Equal(t, "a\nb", got)
}

func TestLastUserContent_NoUserErrors(t *testing.T) {
	msgs := []agent.Message{agent.NewAssistantText("only assistant")}
	_, _, err := lastUserContent(msgs)
	assert.Error(t, err)
}

func TestLastUserContent_SkipsUserWithNoText(t *testing.T) {
	msgs := []agent.Message{
		agent.NewUserText("first"),
		{Role: agent.RoleUser, Content: []agent.Content{{Kind: agent.ContentToolResult}}},
	}
	got, _, err := lastUserContent(msgs)
	require.NoError(t, err)
	assert.Equal(t, "first", got)
}

func TestParseResult_HappyPath(t *testing.T) {
	raw := []byte(`{"type":"result","result":"hello","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3}}`)
	resp, err := parseResult(raw)
	require.NoError(t, err)
	assert.Equal(t, agent.RoleAssistant, resp.Message.Role)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, "hello", resp.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, resp.StopReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 3, resp.Usage.OutputTokens)
}

func TestParseResult_LeadingGarbageBeforeJSON(t *testing.T) {
	raw := []byte(`INFO some log line
{"type":"result","result":"ok","stop_reason":"end_turn"}`)
	resp, err := parseResult(raw)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Message.Content[0].Text)
}

func TestParseResult_ModelError(t *testing.T) {
	raw := []byte(`{"type":"result","is_error":true,"result":"rate limited"}`)
	_, err := parseResult(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
}

func TestParseResult_ModelErrorUsesAPIStatus(t *testing.T) {
	raw := []byte(`{"type":"result","is_error":true,"api_error_status":"overloaded"}`)
	_, err := parseResult(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overloaded")
}

func TestParseResult_NoJSON(t *testing.T) {
	_, err := parseResult([]byte(`not json at all`))
	assert.Error(t, err)
}

func TestParseResult_BadJSON(t *testing.T) {
	_, err := parseResult([]byte(`{invalid`))
	assert.Error(t, err)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hi", truncate("hi", 10))
	assert.Equal(t, "hell…", truncate("hello world", 4))
}

func TestComplete_CommandFailure(t *testing.T) {
	p := New(Config{})
	p.run = func(_ *exec.Cmd) ([]byte, error) {
		return []byte("stderr text"), errors.New("exit status 1")
	}
	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run")
}

func TestComplete_HappyPath(t *testing.T) {
	p := New(Config{
		Model:          "sonnet",
		PermissionMode: "acceptEdits",
		ExtraArgs:      []string{"--extra"},
	})
	var captured *exec.Cmd
	p.run = func(cmd *exec.Cmd) ([]byte, error) {
		captured = cmd
		return []byte(`{"type":"result","result":"ok","stop_reason":"end_turn"}`), nil
	}
	resp, err := p.Complete(context.Background(), agent.Request{
		SessionID: "sess-1",
		System:    "You are a helper",
		Messages:  []agent.Message{agent.NewUserText("hello")},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Message.Content[0].Text)

	require.NotNil(t, captured)
	args := strings.Join(captured.Args, " ")
	assert.Contains(t, args, "--print")
	assert.Contains(t, args, "--session-id sess-1")
	assert.Contains(t, args, "--system-prompt You are a helper")
	assert.Contains(t, args, "--model sonnet")
	assert.Contains(t, args, "--permission-mode acceptEdits")
	assert.Contains(t, args, "--extra")
	assert.Contains(t, args, "hello")
}

func TestComplete_NoUserMessage(t *testing.T) {
	p := New(Config{})
	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewAssistantText("hi")},
	})
	assert.Error(t, err)
}

func TestComplete_SecondTurnUsesResume(t *testing.T) {
	p := New(Config{})
	var calls []string
	p.run = func(cmd *exec.Cmd) ([]byte, error) {
		calls = append(calls, strings.Join(cmd.Args, " "))
		return []byte(`{"type":"result","result":"ok","stop_reason":"end_turn"}`), nil
	}
	req := agent.Request{
		SessionID: "sess-A",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	}
	_, err := p.Complete(context.Background(), req)
	require.NoError(t, err)
	_, err = p.Complete(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, calls, 2)
	assert.Contains(t, calls[0], "--session-id sess-A")
	assert.NotContains(t, calls[0], "--resume")
	assert.Contains(t, calls[1], "--resume sess-A")
	assert.NotContains(t, calls[1], "--session-id")
}

func TestComplete_ColdStartAlreadyInUseFallsBackToResume(t *testing.T) {
	p := New(Config{})
	var calls []string
	p.run = func(cmd *exec.Cmd) ([]byte, error) {
		calls = append(calls, strings.Join(cmd.Args, " "))
		if strings.Contains(calls[len(calls)-1], "--session-id") {
			return []byte("Error: Session ID sess-B is already in use.\n"), errors.New("exit status 1")
		}
		return []byte(`{"type":"result","result":"ok","stop_reason":"end_turn"}`), nil
	}
	_, err := p.Complete(context.Background(), agent.Request{
		SessionID: "sess-B",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	require.Len(t, calls, 2, "should have retried with --resume")
	assert.Contains(t, calls[0], "--session-id sess-B")
	assert.Contains(t, calls[1], "--resume sess-B")
}

func TestComplete_UnrelatedErrorNotRetried(t *testing.T) {
	p := New(Config{})
	var calls int
	p.run = func(_ *exec.Cmd) ([]byte, error) {
		calls++
		return []byte("some other error"), errors.New("exit status 1")
	}
	_, err := p.Complete(context.Background(), agent.Request{
		SessionID: "sess-C",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "should not retry on unrelated errors")
}
