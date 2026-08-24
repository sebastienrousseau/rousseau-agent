package client_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
)

// The tests use a self-hosted mock MCP server implemented as a Go
// program that we compile into a temp binary and spawn. This exercises
// the actual stdio wire path (no in-memory shortcuts) while remaining
// hermetic — no external MCP servers required.

const mockServerSource = `
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type envelope struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id,omitempty\"`" + `
	Method  string          ` + "`json:\"method,omitempty\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
	Result  json.RawMessage ` + "`json:\"result,omitempty\"`" + `
	Error   any             ` + "`json:\"error,omitempty\"`" + `
}

func reply(w *bufio.Writer, id json.RawMessage, result any) {
	res, _ := json.Marshal(result)
	e := envelope{JSONRPC: "2.0", ID: id, Result: res}
	blob, _ := json.Marshal(e)
	w.Write(blob)
	w.WriteByte('\n')
	w.Flush()
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		var env envelope
		if err := json.Unmarshal(in.Bytes(), &env); err != nil {
			continue
		}
		switch env.Method {
		case "initialize":
			reply(out, env.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]string{"name": "mock", "version": "0.0.0"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "notifications/initialized":
			// no-op notification
		case "tools/list":
			reply(out, env.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "echoes the payload",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"msg": map[string]any{"type": "string"},
							},
						},
					},
				},
			})
		case "tools/call":
			var p struct {
				Name      string          ` + "`json:\"name\"`" + `
				Arguments json.RawMessage ` + "`json:\"arguments\"`" + `
			}
			_ = json.Unmarshal(env.Params, &p)
			// Only "echo" gets a reply; anything else is silently
			// dropped so the client observes a timeout.
			if p.Name != "echo" {
				continue
			}
			text := fmt.Sprintf("echo(%s)=%s", p.Name, string(p.Arguments))
			reply(out, env.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
			})
		}
	}
}
`

// buildMockServer compiles the mock MCP server to a temp binary and
// returns its path. Skips the test if the Go toolchain is unavailable.
func buildMockServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(mockServerSource), 0o600); err != nil {
		t.Fatalf("write mock source: %v", err)
	}
	bin := filepath.Join(dir, "mock-mcp-server")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	gobin := findGo()
	if gobin == "" {
		t.Skip("go binary not found on PATH — skipping stdio client tests")
	}
	cmd := exec.Command(gobin, "build", "-o", bin, src) //nolint:gosec // hermetic build of test fixture
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("go build unavailable (%v): %s", err, string(out))
	}
	return bin
}

func TestClient_ListTools(t *testing.T) {
	bin := buildMockServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := client.New(ctx, client.Config{
		Name:    "mock",
		Command: bin,
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer cl.Close() //nolint:errcheck // best-effort cleanup

	tools, err := cl.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("ListTools returned %d tools, want 1", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("tools[0].Name = %q, want echo", tools[0].Name)
	}
	if tools[0].Description == "" {
		t.Error("tools[0].Description empty")
	}
}

func TestClient_CallTool(t *testing.T) {
	bin := buildMockServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := client.New(ctx, client.Config{
		Name:    "mock",
		Command: bin,
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer cl.Close() //nolint:errcheck // best-effort cleanup

	res, err := cl.CallTool(ctx, "echo", map[string]string{"msg": "hi"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("CallTool returned %d content blocks, want 1", len(res.Content))
	}
	if res.Content[0].Type != "text" {
		t.Errorf("content[0].Type = %q, want text", res.Content[0].Type)
	}
	if len(res.Content[0].Text) == 0 {
		t.Error("content[0].Text empty")
	}
}

func TestClient_RequestTimesOut(t *testing.T) {
	bin := buildMockServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := client.New(ctx, client.Config{
		Name:           "mock",
		Command:        bin,
		RequestTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer cl.Close() //nolint:errcheck // best-effort cleanup

	// The mock drops any method it doesn't recognise; our request
	// therefore never gets a response and the per-call timeout fires.
	_, err = cl.CallTool(ctx, "no_such_method", nil)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

func TestClient_MissingCommand(t *testing.T) {
	_, err := client.New(context.Background(), client.Config{
		Name: "no-cmd",
	})
	if err == nil {
		t.Error("expected error when Command is empty")
	}
}

func TestClient_MissingName(t *testing.T) {
	_, err := client.New(context.Background(), client.Config{
		Command: "/bin/true",
	})
	if err == nil {
		t.Error("expected error when Name is empty")
	}
}

// findGo returns the absolute path to the go binary, or "" if it's
// not resolvable. Prefers $GOROOT/bin/go so we don't depend on $PATH
// ordering in CI.
func findGo() string {
	if r := os.Getenv("GOROOT"); r != "" {
		bin := filepath.Join(r, "bin", "go")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		if _, err := os.Stat(bin); err == nil {
			return bin
		}
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return ""
}
