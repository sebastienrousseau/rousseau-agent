package composio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// action is a rousseau tool that maps 1:1 onto a Composio Action.
// One instance per Composio action; construction happens at daemon
// startup from Client.List.
type action struct {
	c      *Client
	spec   Action
	toolID string
}

// Name implements tools.Tool. Composio action names ("GMAIL_SEND_MESSAGE")
// are UPPER_SNAKE and match the `^[a-z][a-z0-9_]*$` requirement
// after lower-casing.
func (a *action) Name() string { return a.toolID }

// Description implements tools.Tool.
func (a *action) Description() string {
	if a.spec.Description == "" {
		return "Composio-brokered action " + a.spec.Name + " on app " + a.spec.AppKey
	}
	return a.spec.Description
}

// InputSchema implements tools.Tool. Composio's parameters field is
// already a JSON Schema; pass through verbatim.
func (a *action) InputSchema() map[string]any {
	if len(a.spec.Parameters) == 0 {
		return map[string]any{"type": "object"}
	}
	var m map[string]any
	if err := json.Unmarshal(a.spec.Parameters, &m); err != nil {
		return map[string]any{"type": "object"}
	}
	return m
}

// Execute implements tools.Tool. The model's input is proxied
// verbatim into Composio's execute endpoint; the returned JSON
// becomes the tool result.
func (a *action) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	body, err := a.c.Execute(ctx, a.spec.Name, input)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Register discovers every Composio action visible to the
// authenticated user and registers one rousseau tool per action.
// Errors from the discovery step surface at daemon startup so
// operators see them before serving traffic.
//
// The apps parameter, when non-empty, restricts registration to the
// named Composio apps (case-insensitive). Empty registers every
// visible action — useful for exploration, dangerous for auditing.
func Register(ctx context.Context, reg *tools.Registry, c *Client, apps []string) (int, error) {
	actions, err := c.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("composio: discover actions: %w", err)
	}
	filter := make(map[string]bool, len(apps))
	for _, a := range apps {
		filter[strings.ToLower(a)] = true
	}
	registered := 0
	for _, spec := range actions {
		if len(filter) > 0 && !filter[strings.ToLower(spec.AppKey)] {
			continue
		}
		t := &action{
			c:      c,
			spec:   spec,
			toolID: toToolID(spec.AppKey, spec.Name),
		}
		if err := reg.Register(t); err != nil {
			// Duplicate name — Composio actions are supposed to be
			// unique, but a rousseau operator might have also enabled
			// a native suite with an overlapping tool. Skip rather
			// than fail; log via the registered set.
			continue
		}
		registered++
	}
	return registered, nil
}

// toToolID normalises a Composio action name into a rousseau tool
// name. rousseau requires `^[a-z][a-z0-9_]*$`; Composio names are
// UPPER_SNAKE, so lower-case them and prefix with `cx_` to make the
// origin obvious in log output.
func toToolID(app, name string) string {
	lowered := strings.ToLower(name)
	// Replace anything non-{a-z,0-9,_} with underscore.
	var b strings.Builder
	b.WriteString("cx_")
	if app != "" {
		lowerApp := strings.ToLower(app)
		for _, r := range lowerApp {
			if isIDRune(r) {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
		b.WriteByte('_')
	}
	for _, r := range lowered {
		if isIDRune(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func isIDRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_'
}
