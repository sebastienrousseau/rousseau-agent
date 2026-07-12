// Package signal implements transport.Transport on top of signal-cli
// (https://github.com/AsamK/signal-cli) in its JSON-RPC daemon mode.
//
// signal-cli speaks JSON-RPC 2.0 over stdin/stdout when invoked with
// `--output=json jsonRpc`. Outbound `send` requests deliver messages;
// inbound messages arrive as `receive` notifications. Requires the
// operator to have already registered / linked signal-cli against the
// target account.
package signal

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Config configures the Signal transport.
type Config struct {
	// Binary is the signal-cli executable to invoke. Empty defaults to
	// "signal-cli".
	Binary string
	// Account is the E.164 phone number the daemon runs as
	// (e.g. "+15551234567"). Required.
	Account string
	// ExtraArgs are inserted before the `jsonRpc` subcommand — useful
	// for `--config <path>` or `--verbose`.
	ExtraArgs []string
	// ReplyHeader is prepended to every outbound reply. Empty leaves
	// the message body unmodified.
	ReplyHeader string
}

// Client is a transport.Transport backed by signal-cli.
type Client struct {
	cfg     Config
	logger  *slog.Logger
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   *jsonWriter
	stopped atomic.Bool
	nextID  atomic.Uint64
}

// New constructs a Client. Account is required.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.Account == "" {
		return nil, errors.New("signal: Account is required")
	}
	if cfg.Binary == "" {
		cfg.Binary = "signal-cli"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{cfg: cfg, logger: logger}, nil
}

// Name returns the transport identifier.
func (*Client) Name() string { return "signal" }

// Start spawns signal-cli in JSON-RPC mode and pumps messages to
// handler until ctx is cancelled or Stop is called.
func (c *Client) Start(ctx context.Context, handler transport.Handler) error {
	if handler == nil {
		return errors.New("signal: handler is required")
	}
	c.mu.Lock()
	if c.cmd != nil {
		c.mu.Unlock()
		return errors.New("signal: already started")
	}
	args := append([]string{"--output=json", "-a", c.cfg.Account}, c.cfg.ExtraArgs...)
	args = append(args, "jsonRpc")
	cmd := exec.CommandContext(ctx, c.cfg.Binary, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("signal: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("signal: stdout pipe: %w", err)
	}
	cmd.Stderr = &prefixWriter{logger: c.logger}

	if err := cmd.Start(); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("signal: start signal-cli: %w", err)
	}
	c.cmd = cmd
	c.stdin = &jsonWriter{w: stdin, enc: json.NewEncoder(stdin)}
	c.mu.Unlock()

	c.logger.Info("signal.started", slog.String("account", c.cfg.Account))
	err = c.pump(ctx, stdout, handler)
	_ = c.Stop()
	return err
}

// Stop signals signal-cli to exit. Safe to call multiple times.
func (c *Client) Stop() error {
	if c.stopped.Swap(true) {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return nil
}

// Deliver sends a plain text message to the given recipient (E.164
// phone number or Signal group id). Suitable as a cron.Delivery target.
func (c *Client) Deliver(ctx context.Context, recipient, body string) error {
	c.mu.Lock()
	w := c.stdin
	c.mu.Unlock()
	if w == nil {
		return errors.New("signal: not connected")
	}
	if c.cfg.ReplyHeader != "" {
		body = c.cfg.ReplyHeader + body
	}
	id := c.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "send",
		Params: map[string]any{
			"recipient": []string{recipient},
			"message":   body,
		},
	}
	_ = ctx // signal-cli writes are best-effort; no per-request timeout wired
	return w.write(req)
}

// pump reads JSON-RPC frames from stdout and routes incoming messages
// to the handler.
func (c *Client) pump(ctx context.Context, stdout io.Reader, handler transport.Handler) error {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.handleFrame(ctx, scanner.Bytes(), handler); err != nil {
			c.logger.Warn("signal.frame_failed", slog.String("err", err.Error()))
		}
	}
	return scanner.Err()
}

func (c *Client) handleFrame(ctx context.Context, raw []byte, handler transport.Handler) error {
	var env jsonRPCEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("parse frame: %w", err)
	}
	if env.Method != "receive" {
		return nil // ignore acks and other traffic
	}
	var params receiveParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return fmt.Errorf("parse receive params: %w", err)
	}
	body := strings.TrimSpace(params.Envelope.DataMessage.Message)
	if body == "" {
		return nil
	}
	msg := transport.IncomingMessage{
		From: params.Envelope.SourceNumber,
		Body: body,
		At:   time.UnixMilli(params.Envelope.Timestamp),
	}
	if msg.From == "" {
		msg.From = params.Envelope.Source
	}
	c.logger.Info("signal.incoming", slog.String("from", msg.From))
	reply, err := handler.Handle(ctx, msg)
	if err != nil {
		c.logger.Error("signal.handler_failed", slog.String("err", err.Error()))
		return nil
	}
	if reply == "" {
		return nil
	}
	return c.Deliver(ctx, msg.From, reply)
}

// -- JSON-RPC helpers ------------------------------------------------

type jsonWriter struct {
	w   io.WriteCloser
	enc *json.Encoder
	mu  sync.Mutex
}

func (jw *jsonWriter) write(v any) error {
	jw.mu.Lock()
	defer jw.mu.Unlock()
	return jw.enc.Encode(v)
}

func (jw *jsonWriter) Close() error { return jw.w.Close() }

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type receiveParams struct {
	Envelope receiveEnvelope `json:"envelope"`
	Account  string          `json:"account"`
}

type receiveEnvelope struct {
	Source       string             `json:"source"`
	SourceNumber string             `json:"sourceNumber"`
	Timestamp    int64              `json:"timestamp"`
	DataMessage  receiveDataMessage `json:"dataMessage"`
}

type receiveDataMessage struct {
	Message string `json:"message"`
}

// prefixWriter routes signal-cli's stderr into our structured logger.
type prefixWriter struct {
	logger *slog.Logger
	buf    []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		nl := indexByte(p.buf, '\n')
		if nl < 0 {
			break
		}
		line := strings.TrimSpace(string(p.buf[:nl]))
		p.buf = p.buf[nl+1:]
		if line != "" {
			p.logger.Debug("signal.stderr", slog.String("line", line))
		}
	}
	return len(b), nil
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// Compile-time interface satisfaction check.
var _ transport.Transport = (*Client)(nil)
