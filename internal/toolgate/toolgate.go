// Package toolgate lets rousseau's approval policy govern tools that a
// provider subprocess runs on its own. The claude CLI executes Bash,
// Edit, Write and MCP tools inside its own loop, so the agent's
// Approver never sees them. toolgate closes that gap:
//
//   - The daemon runs a Server on a private unix socket that answers
//     "may this tool call run?" with the same Approver chain (pattern,
//     RBAC, OPA, multi-party) and audit trail as native tool calls.
//   - claude is started with a PreToolUse hook that runs
//     `rousseau hook pre-tool-use --socket <path>`; RunHook forwards
//     the call to the Server and blocks it (exit 2) on deny.
//
// Every failure on the hook side (unreadable input, socket missing,
// timeout, malformed reply) denies: the gate fails closed.
package toolgate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Request is one pending tool call, as claude's PreToolUse hook
// reports it on stdin (unknown fields are ignored).
type Request struct {
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// Response is the Server's verdict.
type Response struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// DecideFunc evaluates one request. It may block (multi-party approval
// waits for votes) and must honour ctx.
type DecideFunc func(ctx context.Context, req Request) Response

// maxRequestBytes bounds one request line (tool inputs such as a Write
// of a large file can be big, but not unbounded).
const maxRequestBytes = 8 << 20

// requestReadTimeout bounds how long a connection may take to send its
// request line. A client that connects and stalls would otherwise pin
// a handler goroutine for good, and Close waits on every handler. A
// var so tests can shorten it.
var requestReadTimeout = 10 * time.Second

// Server answers tool-call decisions on a unix socket.
type Server struct {
	ln      net.Listener
	dir     string
	path    string
	decide  DecideFunc
	timeout time.Duration
	logger  *slog.Logger
	wg      sync.WaitGroup
	once    sync.Once
}

// Listen creates a private (0700) directory under os.TempDir, binds a
// socket in it, and returns a Server that is not yet serving. timeout
// bounds each decision (it should cover a multi-party approval wait).
func Listen(decide DecideFunc, timeout time.Duration, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	// Short prefix: unix socket paths are limited to ~108 bytes.
	dir, err := os.MkdirTemp("", "rtg-")
	if err != nil {
		return nil, fmt.Errorf("toolgate: socket dir: %w", err)
	}
	path := filepath.Join(dir, "gate.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir) //nolint:errcheck // best-effort rollback
		return nil, fmt.Errorf("toolgate: listen: %w", err)
	}
	return &Server{ln: ln, dir: dir, path: path, decide: decide, timeout: timeout, logger: logger}, nil
}

// Path is the socket path to hand to the hook command.
func (s *Server) Path() string { return s.path }

// Serve accepts connections until Close. Each connection carries one
// request line and receives one response line.
func (s *Server) Serve(ctx context.Context) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, conn)
		}()
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()                                           //nolint:errcheck // one-shot connection
	_ = conn.SetReadDeadline(time.Now().Add(requestReadTimeout)) //nolint:errcheck // a failed deadline surfaces as a read error
	r := bufio.NewReaderSize(conn, 64*1024)
	line, err := readLine(r, maxRequestBytes)
	resp := Response{Allow: false, Reason: "toolgate: unreadable request"}
	if err == nil {
		var req Request
		if jerr := json.Unmarshal(line, &req); jerr != nil || req.ToolName == "" {
			resp.Reason = "toolgate: malformed request"
		} else {
			dctx, cancel := context.WithTimeout(ctx, s.timeout)
			resp = s.decide(dctx, req)
			cancel()
			s.logger.Debug("toolgate.decision",
				slog.String("session_id", req.SessionID),
				slog.String("tool", req.ToolName),
				slog.Bool("allow", resp.Allow))
		}
	}
	out, _ := json.Marshal(resp)         //nolint:errcheck // Response always marshals
	_, _ = conn.Write(append(out, '\n')) //nolint:errcheck // client treats a missing reply as deny
}

// Close stops accepting, waits for in-flight decisions, and removes
// the socket directory.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		err = s.ln.Close()
		s.wg.Wait()
		_ = os.RemoveAll(s.dir) //nolint:errcheck // best-effort cleanup of a temp dir
	})
	return err
}

func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		line = append(line, frag...)
		if len(line) > max {
			return nil, errors.New("toolgate: request too large")
		}
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return line, nil
		default:
			return nil, err
		}
	}
}

// Exit codes RunHook returns, per claude's hook contract: 0 lets the
// tool run; 2 blocks it and shows stderr to the model.
const (
	ExitAllow = 0
	ExitBlock = 2
)

// RunHook is the body of `rousseau hook pre-tool-use`: it reads the
// hook event from stdin, asks the Server at socket, and returns the
// exit code. Anything short of an explicit allow blocks.
func RunHook(stdin io.Reader, stderr io.Writer, socket string, timeout time.Duration) int {
	block := func(reason string) int {
		_, _ = fmt.Fprintln(stderr, reason) //nolint:errcheck // nothing left to report to if stderr fails; the exit code still blocks
		return ExitBlock
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, maxRequestBytes+1))
	if err != nil || len(raw) > maxRequestBytes {
		return block("rousseau policy: unreadable tool call; blocked")
	}
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil || req.ToolName == "" {
		return block("rousseau policy: malformed tool call; blocked")
	}
	line, _ := json.Marshal(req) //nolint:errcheck // Request always marshals

	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return block("rousseau policy: approval service unreachable; blocked")
	}
	defer conn.Close()                            //nolint:errcheck // one-shot connection
	_ = conn.SetDeadline(time.Now().Add(timeout)) //nolint:errcheck // a failed deadline surfaces as a read error
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return block("rousseau policy: approval service write failed; blocked")
	}
	respLine, err := readLine(bufio.NewReader(conn), 64*1024)
	if err != nil {
		return block("rousseau policy: no decision received; blocked")
	}
	var resp Response
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return block("rousseau policy: malformed decision; blocked")
	}
	if !resp.Allow {
		reason := resp.Reason
		if reason == "" {
			reason = "denied by policy"
		}
		return block("rousseau policy: " + reason)
	}
	return ExitAllow
}

// HookSettings returns the JSON for claude's --settings flag that runs
// hookCommand before every tool call. timeoutSec is claude's per-hook
// limit and should match the Server's decision timeout.
func HookSettings(hookCommand string, timeoutSec int) (string, error) {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Matcher string `json:"matcher"`
		Hooks   []hook `json:"hooks"`
	}
	b, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []matcher{{Matcher: "*", Hooks: []hook{{Type: "command", Command: hookCommand, Timeout: timeoutSec}}}},
		},
	})
	return string(b), err
}
