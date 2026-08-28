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

// Structured runs a completion and enforces that the model's output
// parses as JSON matching the supplied Schema. Provider-native
// constrained decoding (Anthropic tool-use, OpenAI response_format,
// Vertex responseSchema) is not yet plumbed; today the helper falls
// back to prompting with the schema plus a strict "reply with ONLY
// JSON" instruction, then parses the first JSON object out of the
// response.
func Structured(ctx context.Context, provider Provider, req StructuredRequest) (StructuredResponse, error) {
	if provider == nil {
		return StructuredResponse{}, errors.New("agent: nil provider")
	}
	if len(req.Schema) == 0 {
		return StructuredResponse{}, errors.New("agent: empty schema")
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
	return StructuredResponse{Raw: text, Parsed: obj}, nil
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
