package model

// EstimateTokens approximates the prompt tokens a message list will
// cost. It is deliberately simple (about four characters per token
// for text and JSON, a flat charge per image) so callers can trigger
// compression and guard budgets without a provider round-trip; it
// over-estimates slightly for English prose and under-estimates for
// CJK text, which is the safe direction for a budget check only in
// the first case. Providers report exact usage after the fact.
func EstimateTokens(msgs []Message) int {
	chars := 0
	images := 0
	for _, m := range msgs {
		for _, c := range m.Content {
			chars += len(c.Text)
			if c.ToolUse != nil {
				chars += len(c.ToolUse.Name) + len(c.ToolUse.Input)
			}
			if c.ToolResult != nil {
				chars += len(c.ToolResult.Output)
			}
			if c.Image != nil {
				images++
			}
		}
		// Role and framing overhead per message.
		chars += 16
	}
	return chars/charsPerToken + images*tokensPerImage
}

const (
	charsPerToken  = 4
	tokensPerImage = 1600
)
