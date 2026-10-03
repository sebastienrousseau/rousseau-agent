package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// StructuredRequest asks a Provider for a completion whose text
// parses cleanly into the supplied JSON Schema.
type StructuredRequest struct {
	// SystemPrompt is prepended to the base instructions. Empty means
	// no system-prompt component.
	SystemPrompt string
	// Prompt is the user-facing instruction — what the model should
	// produce. The schema is appended as guidance.
	Prompt string
	// Schema is a JSON Schema object describing the target shape.
	Schema map[string]any
}

// StructuredResponse carries the parsed value and the raw text the
// provider returned. Callers that want to log or debug can inspect
// Raw; callers that want the typed value can decode Target inside
// their own handler.
type StructuredResponse struct {
	Raw    string
	Parsed json.RawMessage
}

// StructuredCompleter is implemented by providers with native
// schema-constrained output (claudecli --json-schema). The returned
// JSON conforms to schema as enforced by the provider.
type StructuredCompleter interface {
	CompleteStructured(ctx context.Context, req Request, schema map[string]any) (json.RawMessage, error)
}

// Structured runs a completion whose output must be JSON matching the
// supplied Schema. Providers implementing StructuredCompleter use
// their native constrained decoding; others fall back to prompting
// with the schema plus a strict "reply with ONLY JSON" instruction and
// parsing the first JSON value out of the reply. Either way the result
// is then checked against the schema's required properties, property
// types and enums (see validateAgainstSchema).
func Structured(ctx context.Context, provider Provider, req StructuredRequest) (StructuredResponse, error) {
	if provider == nil {
		return StructuredResponse{}, errors.New("agent: nil provider")
	}
	if len(req.Schema) == 0 {
		return StructuredResponse{}, errors.New("agent: empty schema")
	}
	if sc, ok := provider.(StructuredCompleter); ok {
		out, err := sc.CompleteStructured(ctx, Request{
			System:   strings.TrimSpace(req.SystemPrompt),
			Messages: []Message{NewUserText(req.Prompt)},
		}, req.Schema)
		if err != nil {
			return StructuredResponse{}, fmt.Errorf("agent: structured: %w", err)
		}
		if err := validateAgainstSchema(out, req.Schema); err != nil {
			return StructuredResponse{}, fmt.Errorf("agent: structured: %w", err)
		}
		return StructuredResponse{Raw: string(out), Parsed: out}, nil
	}
	schemaBytes, err := json.MarshalIndent(req.Schema, "", "  ")
	if err != nil {
		return StructuredResponse{}, fmt.Errorf("agent: schema: %w", err)
	}
	sys := strings.TrimSpace(req.SystemPrompt) + "\n\n" +
		"You reply with a single JSON value that matches this JSON Schema exactly:\n\n" +
		string(schemaBytes) + "\n\n" +
		"Rules:\n" +
		"- Output ONLY the JSON value. No preamble, no explanation, no code fences.\n" +
		"- Every required property MUST be present.\n" +
		"- Use exactly the property names in the schema; no extras."
	resp, err := provider.Complete(ctx, Request{
		System:   strings.TrimSpace(sys),
		Messages: []Message{NewUserText(req.Prompt)},
	})
	if err != nil {
		return StructuredResponse{}, fmt.Errorf("agent: structured: %w", err)
	}
	text := firstText(resp.Message)
	if text == "" {
		return StructuredResponse{}, errors.New("agent: provider returned no text")
	}
	obj, err := extractJSON(text)
	if err != nil {
		return StructuredResponse{}, fmt.Errorf("agent: parse JSON: %w: %s", err, truncateForError(text, 200))
	}
	if err := validateAgainstSchema(obj, req.Schema); err != nil {
		return StructuredResponse{}, fmt.Errorf("agent: structured: %w", err)
	}
	return StructuredResponse{Raw: text, Parsed: obj}, nil
}

// validateAgainstSchema checks the subset of JSON Schema that typed
// outputs rely on: an object's required properties, and each declared
// property's "type" (string, number, integer, boolean, array, object)
// and "enum". It is not a full validator; it exists so a prompt-only
// provider's drift (a missing field, an out-of-set label) is an error
// rather than a silently wrong value.
func validateAgainstSchema(raw json.RawMessage, schema map[string]any) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("output is not JSON: %w", err)
	}
	return checkValue(v, schema, "$")
}

func checkValue(v any, schema map[string]any, path string) error {
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: %v is not one of %v", path, v, enum)
		}
	}
	switch t, _ := schema["type"].(string); t {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want object", path)
		}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if _, present := obj[fmt.Sprint(r)]; !present {
					return fmt.Errorf("%s: missing required property %q", path, r)
				}
			}
		}
		if req, ok := schema["required"].([]string); ok {
			for _, r := range req {
				if _, present := obj[r]; !present {
					return fmt.Errorf("%s: missing required property %q", path, r)
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for name, ps := range props {
			sub, ok := ps.(map[string]any)
			val, present := obj[name]
			if !ok || !present {
				continue
			}
			if err := checkValue(val, sub, path+"."+name); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s: want string", path)
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("%s: want number", path)
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("%s: want integer", path)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: want boolean", path)
		}
	case "array":
		if _, ok := v.([]any); !ok {
			return fmt.Errorf("%s: want array", path)
		}
	}
	return nil
}

// firstText returns the first non-empty ContentText block of a
// Message.
func firstText(m Message) string {
	for _, c := range m.Content {
		if c.Kind == ContentText && strings.TrimSpace(c.Text) != "" {
			return c.Text
		}
	}
	return ""
}

// extractJSON scans text for the first top-level `{...}` or `[...]`
// value and returns it as a json.RawMessage. Models occasionally leak
// a markdown fence or a leading "Here is the JSON:" line even when
// instructed not to; this pass shrugs those off.
func extractJSON(text string) (json.RawMessage, error) {
	// Strip a markdown code fence if present.
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		nl := strings.IndexByte(trimmed, '\n')
		if nl > 0 {
			trimmed = trimmed[nl+1:]
		}
		if end := strings.LastIndex(trimmed, "```"); end > 0 {
			trimmed = trimmed[:end]
		}
	}
	trimmed = strings.TrimSpace(trimmed)

	// Locate the first { or [ and last matching close. Naive but
	// robust for well-behaved provider output.
	first := indexJSONStart(trimmed)
	if first < 0 {
		return nil, errors.New("no JSON value found")
	}
	closer := matchingCloser(trimmed[first])
	last := strings.LastIndexByte(trimmed, closer)
	if last < first {
		return nil, errors.New("no matching JSON close")
	}
	candidate := trimmed[first : last+1]
	var validate any
	if err := json.Unmarshal([]byte(candidate), &validate); err != nil {
		return nil, err
	}
	return json.RawMessage(candidate), nil
}

func indexJSONStart(s string) int {
	for i, r := range s {
		if r == '{' || r == '[' {
			return i
		}
	}
	return -1
}

func matchingCloser(open byte) byte {
	if open == '[' {
		return ']'
	}
	return '}'
}

func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
