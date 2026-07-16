package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// The stream event shape lives in package agent so both providers can
// speak the same language. Re-exported here for backwards-compat.
type (
	// StreamEvent aliases agent.StreamEvent.
	StreamEvent = agent.StreamEvent
	// StreamEventKind aliases agent.StreamEventKind.
	StreamEventKind = agent.StreamEventKind
)

// Re-exports of the event-kind constants.
const (
	StreamStart     = agent.StreamStart
	StreamTextDelta = agent.StreamTextDelta
	StreamToolUse   = agent.StreamToolUse
	StreamResult    = agent.StreamResult
	StreamOther     = agent.StreamOther
)

// Stream runs claude in streaming mode and delivers a StreamEvent for
// every NDJSON line the CLI emits, followed by the final Response.
//
// The events channel is closed before Stream returns. Callers MUST
// drain it to avoid leaking the parser goroutine.
//
// Streaming is claudecli's internal fast-feedback path. It is not part
// of the abstract agent.Provider surface — request cadence to the
// model is identical to Complete, but a caller (e.g. the WhatsApp
// daemon) can observe progress without waiting for --output-format json
// to buffer the whole response.
func (p *Provider) Stream(ctx context.Context, req agent.Request) (<-chan agent.StreamEvent, <-chan agent.StreamReport, error) {
	prompt, images, err := lastUserContent(req.Messages)
	if err != nil {
		return nil, nil, err
	}
	imagePaths, cleanup, err := writeImages(images)
	if err != nil {
		return nil, nil, err
	}
	// Cleanup runs when the caller closes the returned channels via
	// ctx cancel; the child process finishes reading the image files
	// well before the JSON stream completes.
	defer cleanup()

	sessionFlag := "--session-id"
	if req.SessionID != "" && p.knowsSession(req.SessionID) {
		sessionFlag = "--resume"
	}

	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose", // stream-json requires --verbose
	}
	if req.SessionID != "" {
		args = append(args, sessionFlag, req.SessionID)
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
	for _, path := range imagePaths {
		args = append(args, "--image", path)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, p.cfg.Binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("claudecli: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("claudecli: start: %w", err)
	}

	events := make(chan agent.StreamEvent, 16)
	report := make(chan agent.StreamReport, 1)

	go func() {
		defer close(events)
		defer close(report)
		resp, perr := parseStream(stdout, events)
		waitErr := cmd.Wait()
		if perr == nil && waitErr != nil {
			// The CLI exited non-zero with no parseable result — surface
			// the stderr for the caller.
			perr = fmt.Errorf("claudecli: stream exit: %w: %s", waitErr, truncate(stderr.String(), 400))
		}
		if perr == nil && req.SessionID != "" {
			p.rememberSession(req.SessionID)
		}
		report <- agent.StreamReport{Response: resp, Err: perr}
	}()

	return events, report, nil
}

// StreamResultReport is retained as an alias for agent.StreamResult
// so pre-refactor callers keep compiling.
type StreamResultReport = agent.StreamReport

// Compile-time check that Provider satisfies agent.StreamingProvider.
var _ agent.StreamingProvider = (*Provider)(nil)

// parseStream reads NDJSON from r, translates each line into a
// StreamEvent (delivered on events), and returns the final Response
// once the terminal "result" line arrives. The events channel is NOT
// closed by parseStream; the caller owns its lifetime.
func parseStream(r io.Reader, events chan<- agent.StreamEvent) (agent.Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var final agent.Response
	var haveResult bool

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		raw := append(json.RawMessage(nil), line...)
		kind, delta, res, isResult := classifyLine(raw)
		events <- agent.StreamEvent{Kind: kind, Delta: delta, Raw: raw}
		if isResult {
			final = res
			haveResult = true
		}
	}
	if err := scanner.Err(); err != nil {
		return agent.Response{}, fmt.Errorf("claudecli: read stream: %w", err)
	}
	if !haveResult {
		return agent.Response{}, errors.New("claudecli: stream ended without a result line")
	}
	return final, nil
}

// classifyLine maps a single NDJSON envelope to a StreamEvent. It is
// deliberately liberal: unknown types return StreamOther so callers can
// still forward the raw payload.
func classifyLine(raw json.RawMessage) (kind agent.StreamEventKind, delta string, final agent.Response, isResult bool) {
	var head struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		Delta   struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return StreamOther, "", agent.Response{}, false
	}
	switch head.Type {
	case "system":
		return StreamStart, "", agent.Response{}, false
	case "assistant":
		if len(head.Message) > 0 {
			if d, ok := extractTextDelta(head.Message); ok {
				return StreamTextDelta, d, agent.Response{}, false
			}
			if hasToolUse(head.Message) {
				return StreamToolUse, "", agent.Response{}, false
			}
		}
		if head.Delta.Type == "text_delta" && head.Delta.Text != "" {
			return StreamTextDelta, head.Delta.Text, agent.Response{}, false
		}
		return StreamOther, "", agent.Response{}, false
	case "user":
		return StreamOther, "", agent.Response{}, false
	case "result":
		res, err := parseResult(raw)
		if err != nil {
			return StreamOther, "", agent.Response{}, false
		}
		return StreamResult, "", res, true
	default:
		return StreamOther, "", agent.Response{}, false
	}
}

// extractTextDelta scans an assistant message for a `content` array
// and returns the concatenation of its text blocks.
func extractTextDelta(msg json.RawMessage) (string, bool) {
	var m struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return "", false
	}
	var out strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" && c.Text != "" {
			out.WriteString(c.Text)
		}
	}
	if out.Len() == 0 {
		return "", false
	}
	return out.String(), true
}

func hasToolUse(msg json.RawMessage) bool {
	var m struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return false
	}
	for _, c := range m.Content {
		if c.Type == "tool_use" {
			return true
		}
	}
	return false
}
