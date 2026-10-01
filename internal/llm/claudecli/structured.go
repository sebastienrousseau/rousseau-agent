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

// CompleteStructured implements agent.StructuredCompleter with claude's
// native --json-schema, returning the CLI's validated structured_output.
//
// The call is one-shot and tool-less: --tools "" disables every
// built-in tool, --strict-mcp-config with no --mcp-config loads no MCP
// servers, and the policy-bridge settings are not passed. A decision
// call (e.g. a risk-scoring approver) must not be able to run tools,
// or it would trigger the PreToolUse hook and recurse into the
// approver that made it.
func (p *Provider) CompleteStructured(ctx context.Context, req agent.Request, schema map[string]any) (json.RawMessage, error) {
	prompt, _, err := lastUserContent(req.Messages)
	if err != nil {
		return nil, err
	}
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("claudecli: structured: schema: %w", err)
	}
	args := []string{"--print", "--output-format", "json",
		"--json-schema", string(schemaJSON),
		"--tools", "", "--strict-mcp-config"}
	if req.System != "" {
		args = append(args, "--system-prompt", req.System)
	}
	if p.cfg.Model != "" {
		args = append(args, "--model", p.cfg.Model)
	}
	cmd := exec.CommandContext(ctx, p.cfg.Binary, args...)
	cmd.Stdin = strings.NewReader(prompt)
	setGracefulCancel(cmd)
	out, err := p.run(cmd)
	if err != nil {
		return nil, fmt.Errorf("claudecli: structured: run: %w: %s", err, truncate(string(out), 400))
	}
	i := bytes.IndexByte(out, '{')
	if i < 0 {
		return nil, fmt.Errorf("claudecli: structured: no JSON in output: %s", truncate(string(out), 200))
	}
	var res struct {
		IsError          bool            `json:"is_error"`
		Subtype          string          `json:"subtype"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.NewDecoder(bytes.NewReader(out[i:])).Decode(&res); err != nil {
		return nil, fmt.Errorf("claudecli: structured: parse: %w", err)
	}
	if res.IsError {
		return nil, fmt.Errorf("claudecli: structured: %s: %s", res.Subtype, truncate(res.Result, 200))
	}
	if len(res.StructuredOutput) == 0 || string(res.StructuredOutput) == "null" {
		return nil, errors.New("claudecli: structured: no structured_output in the result")
	}
	return res.StructuredOutput, nil
}
