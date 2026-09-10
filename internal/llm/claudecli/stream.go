package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"

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

// ErrEmptyStream is returned by parseStream when the CLI's stdout
// finishes without any type:"result" line and without a per-line
// result-parse error to surface. Exported so callers (the Stream
// goroutine) can promote a more useful error — typically the CLI's
// exit status + stderr — when they see this sentinel.
var ErrEmptyStream = errors.New("claudecli: stream ended without a result line")

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
	// NOTE: cleanup is deliberately NOT deferred here. Stream returns
	// as soon as the child is started, so a deferred cleanup would
	// delete the image files while the CLI is still running -- a
	// use-after-free from the child's point of view, since the images
	// are handed over by path via --image. It is invoked on each early
	// error return below, and otherwise by the reader goroutine once
	// cmd.Wait has returned.

	args := p.buildStreamArgs(req, imagePaths, prompt)

	cmd, stdout, stderr, err := p.startStream(ctx, args)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	events := make(chan agent.StreamEvent, 16)
	report := make(chan agent.StreamReport, 1)

	go p.drainStream(ctx, req, cmd, stdout, stderr, imagePaths, prompt, cleanup, events, report)

	return events, report, nil
}

// buildStreamArgs constructs the argv rousseau hands to `claude`.
// Extracted so the recover path can rebuild it after rotating a
// poisoned session file without duplicating the assembly.
func (p *Provider) buildStreamArgs(req agent.Request, imagePaths []string, prompt string) []string {
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
	return args
}

// startStream launches claude and wires stdout/stderr. Extracted to
// let the recover path re-spawn identically after a rotate.
func (p *Provider) startStream(ctx context.Context, args []string) (*exec.Cmd, io.Reader, *bytes.Buffer, error) {
	cmd := exec.CommandContext(ctx, p.cfg.Binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("claudecli: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("claudecli: start: %w", err)
	}
	return cmd, stdout, &stderr, nil
}

// drainStream runs the parse + wait cycle for a live subprocess and
// finalises the report. Split out from Stream so the session-in-use
// recovery can retry once transparently: same events channel,
// same report channel, same visible API — the caller sees a single
// stream that happens to be sourced from either the first or second
// subprocess attempt depending on whether the rotate succeeded.
func (p *Provider) drainStream(
	ctx context.Context,
	req agent.Request,
	cmd *exec.Cmd,
	stdout io.Reader,
	stderr *bytes.Buffer,
	imagePaths []string,
	prompt string,
	cleanup func(),
	events chan agent.StreamEvent,
	report chan agent.StreamReport,
) {
	// Defer covers panics but the happy path runs cleanup
	// explicitly (before we send the report) so a caller that
	// blocks on <-report has a happens-before edge on the temp
	// files being gone.
	defer cleanup()
	defer close(events)
	defer close(report)
	resp, perr := parseStream(stdout, events)
	waitErr := cmd.Wait()
	// Promote the CLI's exit status + stderr over the empty-stream
	// sentinel: a subprocess that died without emitting a result
	// line almost always explains itself on stderr (auth failure,
	// missing config, killed by signal, etc), and "stream ended
	// without a result line" alone gives operators nothing to
	// diagnose. A per-line resultErr from classifyLine
	// (is_error:true result envelope) is left intact because it is
	// strictly more specific than exit + stderr.
	if waitErr != nil && (perr == nil || errors.Is(perr, ErrEmptyStream)) {
		perr = fmt.Errorf("claudecli: stream exit: %w: %s", waitErr, truncate(stderr.String(), 400))
	}

	// Session-in-use recovery. Rotate the poisoned transcript aside
	// and retry ONCE with the same session id — the caller's JID→
	// session mapping stays valid so subsequent turns keep the same
	// conversation continuity. One-shot: if the retry also fails
	// (session-in-use or otherwise) we surface the retry's error and
	// stop, no chained rotates.
	if isSessionInUseError(perr) && req.SessionID != "" {
		path := sessionFilePathResolver(req.SessionID)
		rotated, rerr := rotateSessionFile(path, time.Now)
		if rerr != nil {
			slog.Default().Warn("claudecli.session_recover_rotate_failed",
				slog.String("session_id", req.SessionID),
				slog.String("path", path),
				slog.String("err", rerr.Error()))
		} else {
			slog.Default().Warn("claudecli.session_in_use_recovered",
				slog.String("session_id", req.SessionID),
				slog.Bool("rotated", rotated),
				slog.String("path", path))
			// Force --session-id for the retry regardless of the
			// cache: post-rotate the transcript file does not exist,
			// so --resume would fail; --session-id creates a fresh
			// file with the same id. Rebuild args after clearing the
			// cache entry so buildStreamArgs picks --session-id.
			p.cache.Forget(req.SessionID)
			args := p.buildStreamArgs(req, imagePaths, prompt)
			cmd2, stdout2, stderr2, serr := p.startStream(ctx, args)
			if serr != nil {
				perr = fmt.Errorf("claudecli: session recover: restart: %w", serr)
			} else {
				resp2, perr2 := parseStream(stdout2, events)
				waitErr2 := cmd2.Wait()
				if waitErr2 != nil && (perr2 == nil || errors.Is(perr2, ErrEmptyStream)) {
					perr2 = fmt.Errorf("claudecli: stream exit: %w: %s", waitErr2, truncate(stderr2.String(), 400))
				}
				resp, perr = resp2, perr2
			}
		}
	}

	if perr == nil && req.SessionID != "" {
		p.rememberSession(req.SessionID)
	}
	cleanup() // now safe: child has exited (cmd.Wait returned)
	report <- agent.StreamReport{Response: resp, Err: perr}
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
	// lastResultErr captures a `type:"result"` line whose parseResult
	// returned an error (is_error:true, or a malformed result JSON).
	// Without this the caller sees the generic "stream ended without
	// a result line" and has no way to know whether the CLI actually
	// emitted a result the parser rejected. Preserved separately from
	// haveResult so the happy path stays untouched.
	var lastResultErr error

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		raw := append(json.RawMessage(nil), line...)
		kind, delta, res, isResult, resultErr := classifyLine(raw)
		events <- agent.StreamEvent{Kind: kind, Delta: delta, Raw: raw}
		if isResult {
			final = res
			haveResult = true
			lastResultErr = nil
		} else if resultErr != nil {
			lastResultErr = resultErr
		}
	}
	if err := scanner.Err(); err != nil {
		return agent.Response{}, fmt.Errorf("claudecli: read stream: %w", err)
	}
	if !haveResult {
		if lastResultErr != nil {
			return agent.Response{}, lastResultErr
		}
		return agent.Response{}, ErrEmptyStream
	}
	return final, nil
}

// classifyLine maps a single NDJSON envelope to a StreamEvent. It is
// deliberately liberal: unknown types return StreamOther so callers can
// still forward the raw payload.
//
// The resultErr return carries the parseResult failure when the line
// was a `type:"result"` envelope the parser rejected (is_error:true,
// truncated JSON, etc). The caller in parseStream promotes this to a
// terminal error when the stream ends without a successful result —
// without it, a legitimate CLI-side error (bad --resume id, model
// error, rate-limit) was masked by the generic "stream ended without
// a result line".
func classifyLine(raw json.RawMessage) (kind agent.StreamEventKind, delta string, final agent.Response, isResult bool, resultErr error) {
	var head struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		Delta   struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		// Malformed line from the CLI stdout (partial write, stray
		// non-JSON text, a future envelope shape we don't recognise) —
		// classify as StreamOther so the caller skips it rather than
		// tearing the whole stream down. A real terminal failure
		// arrives as a well-formed {"type":"result", is_error:true},
		// which is what resultErr is reserved for.
		return StreamOther, "", agent.Response{}, false, nil //nolint:nilerr // deliberate: malformed lines are noise, not a stream-terminating error
	}
	switch head.Type {
	case "system":
		return StreamStart, "", agent.Response{}, false, nil
	case "assistant":
		if len(head.Message) > 0 {
			if d, ok := extractTextDelta(head.Message); ok {
				return StreamTextDelta, d, agent.Response{}, false, nil
			}
			if hasToolUse(head.Message) {
				return StreamToolUse, "", agent.Response{}, false, nil
			}
		}
		if head.Delta.Type == "text_delta" && head.Delta.Text != "" {
			return StreamTextDelta, head.Delta.Text, agent.Response{}, false, nil
		}
		return StreamOther, "", agent.Response{}, false, nil
	case "user":
		return StreamOther, "", agent.Response{}, false, nil
	case "result":
		res, err := parseResult(raw)
		if err != nil {
			return StreamOther, "", agent.Response{}, false, err
		}
		return StreamResult, "", res, true, nil
	default:
		return StreamOther, "", agent.Response{}, false, nil
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
