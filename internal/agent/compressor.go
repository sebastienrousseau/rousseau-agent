package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Compressor rewrites a Session in place so its message count fits
// under a caller-defined budget without losing conversational continuity.
// Implementations are free to summarise via an LLM, drop old messages,
// or (in tests) do nothing.
type Compressor interface {
	// Compress mutates s if the compressor decides it should. Returning
	// (false, nil) is not an error — it means the compressor examined
	// the session and left it alone.
	Compress(ctx context.Context, s *Session) (changed bool, err error)
}

// CompressorFunc adapts a function to Compressor.
type CompressorFunc func(ctx context.Context, s *Session) (bool, error)

// Compress satisfies Compressor.
func (f CompressorFunc) Compress(ctx context.Context, s *Session) (bool, error) {
	return f(ctx, s)
}

// NoopCompressor never rewrites the session. This is the default when
// none is configured — it lets the Agent avoid a nil check on the hot
// path.
type NoopCompressor struct{}

// Compress satisfies Compressor.
func (NoopCompressor) Compress(context.Context, *Session) (bool, error) { return false, nil }

// LLMCompressor summarises the oldest slice of a session by prompting
// a Provider. The condensed text becomes a single synthetic user
// message; the KeepRecent most-recent messages are preserved verbatim
// so the model's context still contains the operative turns.
type LLMCompressor struct {
	// Provider is asked to summarise. Any Provider will do; a cheap
	// model is preferable for cost reasons.
	Provider Provider
	// TriggerMessages is the message count above which Compress
	// engages. Zero disables the size trigger (Compress becomes a
	// no-op for size — a caller may still invoke it deliberately).
	TriggerMessages int
	// KeepRecent is the number of most-recent messages preserved
	// verbatim. Zero uses 8.
	KeepRecent int
	// SummaryPrompt is prepended to the compressed messages when the
	// summary Provider is called. Empty uses a sensible default.
	SummaryPrompt string
	// Marker is a stable phrase inserted at the top of the synthesised
	// summary so a follow-up Compress can identify the already-
	// summarised prefix and avoid recompressing. Empty uses
	// DefaultCompressorMarker.
	Marker string
}

// DefaultCompressorMarker is prepended to synthesised summaries so a
// re-invocation of Compress can tell what to leave alone.
const DefaultCompressorMarker = "[rousseau-compressed]"

const defaultSummaryPrompt = `Summarise the following conversation in <=200 words. Preserve every commitment, TODO, credential, filename, and quoted output. Skip pleasantries. Return only the summary — no preamble.`

// Compress satisfies Compressor.
func (c *LLMCompressor) Compress(ctx context.Context, s *Session) (bool, error) {
	if s == nil || c.Provider == nil {
		return false, nil
	}
	keep := c.KeepRecent
	if keep == 0 {
		keep = 8
	}
	if c.TriggerMessages == 0 || len(s.Messages) < c.TriggerMessages {
		return false, nil
	}
	if len(s.Messages) <= keep {
		return false, nil
	}

	// If the head already carries our marker, another Compress pass
	// happened; drop the pre-marker section and try again on what's
	// left. This bounds runaway growth.
	if c.headAlreadyCompressed(s) && len(s.Messages) < c.TriggerMessages*2 {
		return false, nil
	}

	old := s.Messages[:len(s.Messages)-keep]
	recent := s.Messages[len(s.Messages)-keep:]

	summary, err := c.summarise(ctx, s.ID, old)
	if err != nil {
		return false, err
	}

	marker := c.Marker
	if marker == "" {
		marker = DefaultCompressorMarker
	}

	synthetic := Message{
		Role: RoleUser,
		Content: []Content{{
			Kind: ContentText,
			Text: marker + " (summary of prior " + itoa(len(old)) + " messages):\n\n" + summary,
		}},
		CreatedAt: time.Now().UTC(),
	}
	s.Messages = append([]Message{synthetic}, recent...)
	s.UpdatedAt = time.Now().UTC()
	return true, nil
}

func (c *LLMCompressor) headAlreadyCompressed(s *Session) bool {
	if len(s.Messages) == 0 {
		return false
	}
	marker := c.Marker
	if marker == "" {
		marker = DefaultCompressorMarker
	}
	for _, block := range s.Messages[0].Content {
		if block.Kind == ContentText && strings.HasPrefix(block.Text, marker) {
			return true
		}
	}
	return false
}

// summarise renders the summarisation prompt and asks the Provider.
func (c *LLMCompressor) summarise(ctx context.Context, sessionID string, msgs []Message) (string, error) {
	prompt := c.SummaryPrompt
	if prompt == "" {
		prompt = defaultSummaryPrompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n---\n\n")
	for _, m := range msgs {
		b.WriteString(strings.ToUpper(string(m.Role)))
		b.WriteString(": ")
		for _, c := range m.Content {
			switch c.Kind {
			case ContentText:
				b.WriteString(c.Text)
			case ContentToolUse:
				if c.ToolUse != nil {
					fmt.Fprintf(&b, "[tool_use %s(%s)]", c.ToolUse.Name, string(c.ToolUse.Input))
				}
			case ContentToolResult:
				if c.ToolResult != nil {
					b.WriteString("[tool_result: ")
					b.WriteString(c.ToolResult.Output)
					b.WriteString("]")
				}
			}
			b.WriteString(" ")
		}
		b.WriteString("\n")
	}

	// The Provider is called with a fresh session id so it does not
	// pollute the source session's history.
	req := Request{
		SessionID: "compress-" + sessionID,
		Messages:  []Message{NewUserText(b.String())},
	}
	resp, err := c.Provider.Complete(ctx, req)
	if err != nil {
		return "", fmt.Errorf("compressor: provider: %w", err)
	}
	for _, block := range resp.Message.Content {
		if block.Kind == ContentText && block.Text != "" {
			return strings.TrimSpace(block.Text), nil
		}
	}
	return "", fmt.Errorf("compressor: provider returned no text")
}

// itoa is a tiny copy of strconv.Itoa avoiding a top-level import for
// one call site.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
