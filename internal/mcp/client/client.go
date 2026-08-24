// Package client implements a Model Context Protocol client that
// speaks JSON-RPC 2.0 over line-delimited stdio to an external MCP
// server subprocess (e.g. `@modelcontextprotocol/server-github`,
// `@modelcontextprotocol/server-playwright`).
//
// This is the counterpart to internal/mcp (the server). Together, a
// rousseau daemon can both expose its own tools to external clients
// AND consume tools from external MCP servers registered via config.
//
// Transport: stdio only for now. SSE and HTTP transports will follow
// the same [Client] surface once the remote-MCP protocol stabilises.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp"
)

// Config describes how to spawn and talk to an MCP server subprocess.
type Config struct {
	// Name identifies the server in logs and in tools/list output.
	// Tools registered by an MCP client are prefixed with the name
	// (e.g. "mcp:github:create_issue") so the model can tell them
	// apart from local tools.
	Name string
	// Command is the executable to spawn. Resolved through $PATH.
	Command string
	// Args are the command-line arguments.
	Args []string
	// Env are extra environment variables set on the subprocess in
	// addition to the parent process's environment. Set an entry to
	// "" to unset that variable (matches exec.Cmd semantics).
	Env map[string]string
	// StartTimeout bounds how long we wait for the initialize handshake
	// to complete before killing the subprocess. Zero uses 30s.
	StartTimeout time.Duration
	// RequestTimeout bounds how long a single tools/list or tools/call
	// waits for a response. Zero uses 60s.
	RequestTimeout time.Duration
	// Logger is used for lifecycle + error logs. Nil uses slog.Default.
	Logger *slog.Logger
}

// Client is a running MCP server subprocess connection. All methods
// are safe for concurrent use; requests are correlated by JSON-RPC
// ID via an internal pending-response map.
type Client struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	logger  *slog.Logger
	timeout time.Duration

	// Request/response correlation
	nextID  atomic.Int64
	pending sync.Map // map[int64]chan mcp.Envelope

	// Lifecycle
	closeMu sync.Mutex
	closed  bool
	done    chan struct{}
	// stderrBuf captures the subprocess's stderr for the last N bytes
	// so we can include it in error reports when the subprocess dies
	// unexpectedly. Bounded to prevent unbounded memory growth.
	stderrBuf *boundedBuffer
}

// New spawns the MCP server subprocess described by cfg and completes
// the initialize handshake. Returns a Client ready to serve
// [Client.ListTools] and [Client.CallTool].
//
// Close must be called to stop the subprocess when done.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Command == "" {
		return nil, errors.New("mcp/client: Config.Command is required")
	}
	if cfg.Name == "" {
		return nil, errors.New("mcp/client: Config.Name is required")
	}
	startTimeout := cfg.StartTimeout
	if startTimeout <= 0 {
		startTimeout = 30 * time.Second
	}
	requestTimeout := cfg.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 60 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(slog.String("mcp_server", cfg.Name))

	// #nosec G204 -- cfg.Command is operator-supplied, same trust
	// boundary as any subprocess in the tool registry. Callers vet
	// what MCP servers they enable via config.
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Env = mergeEnv(os.Environ(), cfg.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp/client: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp/client: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp/client: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp/client: start %q: %w", cfg.Command, err)
	}

	stderrBuf := newBoundedBuffer(8 * 1024)
	c := &Client{
		name:      cfg.Name,
		cmd:       cmd,
		stdin:     stdin,
		logger:    logger,
		timeout:   requestTimeout,
		done:      make(chan struct{}),
		stderrBuf: stderrBuf,
	}

	// Drain stderr into the bounded buffer so we can surface it in
	// error reports without letting the subprocess block on a full
	// pipe.
	go drainStderr(stderr, stderrBuf, logger)

	// Read stdout in a background goroutine and route each response
	// envelope to the pending channel keyed by ID.
	go c.readLoop(stdout)

	// Handshake: send initialize, wait for the result, then send
	// notifications/initialized. If any step fails, kill the process
	// and return the error.
	handshakeCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()

	if err := c.initialize(handshakeCtx); err != nil {
		_ = c.Close() //nolint:errcheck // best-effort cleanup after handshake failure
		return nil, fmt.Errorf("mcp/client %s: initialize: %w", cfg.Name, err)
	}

	logger.Info("mcp.client.ready", slog.String("command", cfg.Command))
	return c, nil
}

// Name returns the configured server name.
func (c *Client) Name() string { return c.name }

// ListTools returns the tools advertised by the server via tools/list.
func (c *Client) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	var result mcp.ToolsListResult
	if err := c.request(ctx, mcp.MethodToolsList, nil, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// CallTool invokes name with the given arguments via tools/call.
// arguments is any JSON-serialisable value; nil sends `{}`.
func (c *Client) CallTool(ctx context.Context, name string, arguments any) (mcp.ToolsCallResult, error) {
	rawArgs, err := marshalArgs(arguments)
	if err != nil {
		return mcp.ToolsCallResult{}, fmt.Errorf("mcp/client %s: marshal arguments for %s: %w", c.name, name, err)
	}
	params := mcp.ToolsCallParams{Name: name, Arguments: rawArgs}
	var result mcp.ToolsCallResult
	if err := c.request(ctx, mcp.MethodToolsCall, params, &result); err != nil {
		return mcp.ToolsCallResult{}, err
	}
	return result, nil
}

// Close terminates the subprocess and releases pipe resources.
// Idempotent — repeated calls are no-ops.
func (c *Client) Close() error {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return nil
	}
	c.closed = true
	c.closeMu.Unlock()

	// Best-effort graceful shutdown, then kill.
	_ = c.stdin.Close() //nolint:errcheck // may already be closed
	if c.cmd.Process != nil {
		// Give the process ~1s to exit on stdin EOF before killing.
		exited := make(chan struct{})
		go func() {
			_ = c.cmd.Wait() //nolint:errcheck // exit status irrelevant here
			close(exited)
		}()
		select {
		case <-exited:
		case <-time.After(1 * time.Second):
			_ = c.cmd.Process.Kill() //nolint:errcheck // best-effort
			<-exited
		}
	}
	close(c.done)
	return nil
}

// -- internals -----------------------------------------------------------

// initialize sends the MCP initialize request + the followup
// initialized notification, matching the handshake sequence documented
// in the spec.
func (c *Client) initialize(ctx context.Context) error {
	params := mcp.InitializeParams{
		ProtocolVersion: mcp.ProtocolVersion,
	}
	params.ClientInfo.Name = "rousseau-agent"
	params.ClientInfo.Version = "0.0.1" // TODO: thread the daemon version through
	var result mcp.InitializeResult
	if err := c.request(ctx, mcp.MethodInitialize, params, &result); err != nil {
		return err
	}
	c.logger.Info("mcp.client.initialized",
		slog.String("server_name", result.ServerInfo.Name),
		slog.String("server_version", result.ServerInfo.Version),
		slog.String("protocol_version", result.ProtocolVersion),
	)
	// Fire-and-forget notification (no response expected).
	return c.notify(mcp.MethodInitialized, nil)
}

// request sends an id-carrying request and blocks until the response
// envelope with the matching ID arrives (or ctx cancels, or the
// per-call timeout fires).
func (c *Client) request(ctx context.Context, method string, params, result any) error {
	if c.closedNow() {
		return errors.New("mcp/client: closed")
	}

	id := c.nextID.Add(1)
	respCh := make(chan mcp.Envelope, 1)
	c.pending.Store(id, respCh)
	defer c.pending.Delete(id)

	env, err := buildRequest(id, method, params)
	if err != nil {
		return err
	}
	if err := c.write(env); err != nil {
		return fmt.Errorf("mcp/client %s: write %s: %w", c.name, method, err)
	}

	timeout := c.timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return fmt.Errorf("mcp/client %s: server returned error on %s: [%d] %s", c.name, method, resp.Error.Code, resp.Error.Message)
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("mcp/client %s: decode %s result: %w", c.name, method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("mcp/client %s: %s timed out after %s", c.name, method, timeout)
	case <-c.done:
		return errors.New("mcp/client: closed while awaiting response")
	}
}

// notify sends a JSON-RPC notification (no ID, no response).
func (c *Client) notify(method string, params any) error {
	env := mcp.Envelope{
		JSONRPC: "2.0",
		Method:  method,
	}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("mcp/client %s: marshal notification %s: %w", c.name, method, err)
		}
		env.Params = b
	}
	return c.write(env)
}

// write serialises an envelope and appends the newline stdio delimiter.
func (c *Client) write(env mcp.Envelope) error {
	blob, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("mcp/client %s: marshal: %w", c.name, err)
	}
	blob = append(blob, '\n')
	_, err = c.stdin.Write(blob)
	return err
}

// readLoop reads newline-delimited JSON envelopes from stdout and
// routes responses to their pending channel or logs orphans. Runs
// until stdout closes.
func (c *Client) readLoop(stdout io.Reader) {
	// bufio.Scanner default is 64 KiB — bump for large tools/list
	// responses (github MCP server has ~40 tools with big schemas).
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		var env mcp.Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			c.logger.Warn("mcp.client.decode_error",
				slog.String("err", err.Error()),
				slog.Int("bytes", len(line)),
			)
			continue
		}
		// Notifications from server (no ID) are logged but ignored;
		// we don't subscribe to any today.
		if len(env.ID) == 0 {
			c.logger.Debug("mcp.client.notification",
				slog.String("method", env.Method),
			)
			continue
		}
		var id int64
		if err := json.Unmarshal(env.ID, &id); err != nil {
			c.logger.Warn("mcp.client.non_integer_id",
				slog.String("raw", string(env.ID)),
			)
			continue
		}
		raw, ok := c.pending.Load(id)
		if !ok {
			c.logger.Warn("mcp.client.orphan_response", slog.Int64("id", id))
			continue
		}
		ch := raw.(chan mcp.Envelope)
		// Non-blocking send: if the requester timed out we've cleaned
		// up but the response arrived late.
		select {
		case ch <- env:
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		c.logger.Warn("mcp.client.stdout_scanner", slog.String("err", err.Error()))
	}
	c.logger.Debug("mcp.client.stdout_closed")
}

func (c *Client) closedNow() bool {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	return c.closed
}

// -- helpers -----------------------------------------------------------

func buildRequest(id int64, method string, params any) (mcp.Envelope, error) {
	env := mcp.Envelope{
		JSONRPC: "2.0",
		Method:  method,
	}
	idBytes, err := json.Marshal(id)
	if err != nil {
		return env, fmt.Errorf("mcp/client: marshal id: %w", err)
	}
	env.ID = idBytes
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return env, fmt.Errorf("mcp/client: marshal params for %s: %w", method, err)
		}
		env.Params = b
	}
	return env, nil
}

func marshalArgs(arguments any) (json.RawMessage, error) {
	if arguments == nil {
		return json.RawMessage(`{}`), nil
	}
	if raw, ok := arguments.(json.RawMessage); ok {
		if len(raw) == 0 {
			return json.RawMessage(`{}`), nil
		}
		return raw, nil
	}
	return json.Marshal(arguments)
}

// mergeEnv layers overrides on top of base. Setting an override to ""
// unsets that variable (removes it from the merged list).
func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	unset := make(map[string]bool)
	for k, v := range overrides {
		if v == "" {
			unset[k] = true
		}
	}
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(overrides))
	for _, kv := range base {
		if k, _, ok := splitEnv(kv); ok {
			if _, replace := overrides[k]; replace {
				continue
			}
			if unset[k] {
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		if unset[k] || seen[k] {
			continue
		}
		out = append(out, k+"="+v)
		seen[k] = true
	}
	return out
}

func splitEnv(kv string) (key, value string, ok bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}

// -- bounded stderr capture --------------------------------------------

// boundedBuffer keeps the last N bytes written to it so we can surface
// a subprocess's tail-of-stderr in error reports without letting the
// subprocess accumulate unbounded memory.
type boundedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newBoundedBuffer(max int) *boundedBuffer {
	return &boundedBuffer{max: max}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

// Tail returns the currently-captured stderr as a string.
func (b *boundedBuffer) Tail() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func drainStderr(r io.Reader, buf *boundedBuffer, logger *slog.Logger) {
	_, err := io.Copy(buf, r)
	if err != nil && !errors.Is(err, io.EOF) {
		logger.Debug("mcp.client.stderr_drain_err", slog.String("err", err.Error()))
	}
}
