// Package mcp bridges MCP-server-exposed tools into the agent's
// [tools.Registry]. Given an initialised [client.Client], this package
// discovers the server's tools/list, wraps each as a [tools.Tool]
// backed by [client.Client.CallTool], and registers them with the
// agent registry under the name "mcp:<server-name>:<tool-name>".
//
// The name prefix makes it easy for approvers, telemetry, and log
// consumers to tell MCP-forwarded tools apart from local built-ins.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	mcpwire "github.com/sebastienrousseau/rousseau-agent/internal/mcp"
	"github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// namePrefix is the tool-name prefix applied to every MCP-forwarded
// tool. Exported so approvers can pattern-match on it.
const namePrefix = "mcp:"

// RegisterClient discovers every tool exposed by cl and registers each
// with registry under "mcp:<name>:<tool>". Returns the list of names
// registered so the caller can log a summary. Errors are aggregated —
// a single tool that fails to register does not prevent the others.
func RegisterClient(ctx context.Context, registry *tools.Registry, cl *client.Client) ([]string, error) {
	if registry == nil {
		return nil, errors.New("mcp: nil registry")
	}
	if cl == nil {
		return nil, errors.New("mcp: nil client")
	}

	remoteTools, err := cl.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: list tools for %s: %w", cl.Name(), err)
	}

	registered := make([]string, 0, len(remoteTools))
	var errs []error
	for _, spec := range remoteTools {
		adapter := &Adapter{
			serverName: cl.Name(),
			toolName:   spec.Name,
			desc:       spec.Description,
			schema:     spec.InputSchema,
			client:     cl,
		}
		if err := registry.Register(adapter); err != nil {
			errs = append(errs, fmt.Errorf("register %s: %w", adapter.Name(), err))
			continue
		}
		registered = append(registered, adapter.Name())
	}

	if len(errs) > 0 {
		return registered, errors.Join(errs...)
	}
	return registered, nil
}

// Adapter is a [tools.Tool] that forwards Execute calls to an MCP
// server's tools/call method. Users interact with it exclusively
// through the [tools.Registry].
type Adapter struct {
	serverName string
	toolName   string
	desc       string
	schema     json.RawMessage
	client     *client.Client
}

// Name returns "mcp:<server>:<tool>".
func (a *Adapter) Name() string {
	return namePrefix + a.serverName + ":" + a.toolName
}

// Description returns the MCP server's advertised description,
// prefixed with the server name for clarity in the model's context.
func (a *Adapter) Description() string {
	if a.desc == "" {
		return fmt.Sprintf("[via MCP server %q] (no description)", a.serverName)
	}
	return fmt.Sprintf("[via MCP server %q] %s", a.serverName, a.desc)
}

// InputSchema returns the MCP server's declared input schema. If the
// server sent an empty or malformed schema, we fall back to the
// permissive "object with any properties" shape.
func (a *Adapter) InputSchema() map[string]any {
	if len(a.schema) == 0 {
		return permissiveSchema()
	}
	var schema map[string]any
	if err := json.Unmarshal(a.schema, &schema); err != nil {
		return permissiveSchema()
	}
	return schema
}

// Execute forwards to the MCP server's tools/call. The MCP result is
// serialised to a string suitable for [tools.Tool.Execute]'s return —
// text content blocks are joined with newlines, and IsError=true is
// surfaced as a Go error so the agent's tool-result path marks it.
func (a *Adapter) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	// Empty input becomes "{}" so we always send a valid JSON object
	// as arguments (MCP servers vary in strictness).
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	result, err := a.client.CallTool(ctx, a.toolName, input)
	if err != nil {
		return "", fmt.Errorf("mcp %s: %w", a.Name(), err)
	}
	body := renderContent(result.Content)
	if result.IsError {
		return body, fmt.Errorf("mcp %s: server reported error", a.Name())
	}
	return body, nil
}

// -- helpers -----------------------------------------------------------

func permissiveSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": true,
	}
}

// renderContent joins every text block with a newline. Non-text
// content types are represented by their type marker (e.g.
// "[image content]") until we have callers that need richer forwarding.
func renderContent(cs []mcpwire.Content) string {
	if len(cs) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, c := range cs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if c.Type == "text" {
			sb.WriteString(c.Text)
		} else {
			sb.WriteString("[")
			sb.WriteString(c.Type)
			sb.WriteString(" content]")
		}
	}
	return sb.String()
}

// Compile-time interface satisfaction check.
var _ tools.Tool = (*Adapter)(nil)
