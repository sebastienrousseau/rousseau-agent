package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	mcpadapter "github.com/sebastienrousseau/rousseau-agent/internal/tools/mcp"
)

// The tests reuse the same "mock MCP server" pattern the client package
// uses — compile a tiny Go program with a hard-coded protocol handler
// and drive it end-to-end through the real stdio pipe.

const mockServerSource = `
package main

import (
	"bufio"
	"encoding/json"
	"os"
)

type env struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id,omitempty\"`" + `
	Method  string          ` + "`json:\"method,omitempty\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
	Result  json.RawMessage ` + "`json:\"result,omitempty\"`" + `
}

func reply(w *bufio.Writer, id json.RawMessage, r any) {
	b, _ := json.Marshal(r)
	e := env{JSONRPC: "2.0", ID: id, Result: b}
	x, _ := json.Marshal(e)
	w.Write(x)
	w.WriteByte('\n')
	w.Flush()
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		var e env
		if err := json.Unmarshal(in.Bytes(), &e); err != nil {
			continue
		}
		switch e.Method {
		case "initialize":
			reply(out, e.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]string{"name": "mock", "version": "0.0.0"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "notifications/initialized":
		case "tools/list":
			reply(out, e.ID, map[string]any{
				"tools": []map[string]any{
					{"name": "greet", "description": "greets", "inputSchema": map[string]any{"type": "object"}},
					{"name": "shout", "description": "shouts",  "inputSchema": map[string]any{"type": "object"}},
				},
			})
		case "tools/call":
			var p struct {
				Name string ` + "`json:\"name\"`" + `
			}
			_ = json.Unmarshal(e.Params, &p)
			text := "hello from " + p.Name
			reply(out, e.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
			})
		}
	}
}
`

func buildMockServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(mockServerSource), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	bin := filepath.Join(dir, "mock")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	gobin := findGo()
	if gobin == "" {
		t.Skip("go binary not found on PATH")
	}
	if out, err := exec.Command(gobin, "build", "-o", bin, src).CombinedOutput(); err != nil { //nolint:gosec // hermetic build
		t.Skipf("go build unavailable (%v): %s", err, string(out))
	}
	return bin
}

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

func TestRegisterClient_RegistersAllTools(t *testing.T) {
	bin := buildMockServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := client.New(ctx, client.Config{Name: "mock", Command: bin})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer cl.Close() //nolint:errcheck // best-effort cleanup

	registry := tools.NewRegistry()
	names, err := mcpadapter.RegisterClient(ctx, registry, cl)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("registered %d tools, want 2", len(names))
	}
	// Both prefixed with "mcp:mock:".
	for _, n := range names {
		if !strings.HasPrefix(n, "mcp:mock:") {
			t.Errorf("registered name %q missing mcp:mock: prefix", n)
		}
	}

	// Registered tools are retrievable and produce definitions.
	defs := registry.Definitions()
	if len(defs) != 2 {
		t.Fatalf("Definitions returned %d, want 2", len(defs))
	}
	for _, d := range defs {
		if d.Description == "" {
			t.Errorf("definition for %s has no description", d.Name)
		}
		if _, ok := d.InputSchema["type"]; !ok {
			t.Errorf("definition for %s has no schema type", d.Name)
		}
	}
}

func TestAdapter_ExecuteForwards(t *testing.T) {
	bin := buildMockServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := client.New(ctx, client.Config{Name: "mock", Command: bin})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer cl.Close() //nolint:errcheck // best-effort cleanup

	registry := tools.NewRegistry()
	if _, err := mcpadapter.RegisterClient(ctx, registry, cl); err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}

	tool, ok := registry.Get("mcp:mock:greet")
	if !ok {
		t.Fatal("tool mcp:mock:greet not registered")
	}
	out, err := tool.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "hello from greet") {
		t.Errorf("Execute output = %q, want to contain 'hello from greet'", out)
	}
}

func TestRegisterClient_NilRegistry(t *testing.T) {
	_, err := mcpadapter.RegisterClient(context.Background(), nil, &client.Client{})
	if err == nil {
		t.Error("expected error for nil registry")
	}
}

func TestRegisterClient_NilClient(t *testing.T) {
	_, err := mcpadapter.RegisterClient(context.Background(), tools.NewRegistry(), nil)
	if err == nil {
		t.Error("expected error for nil client")
	}
}
