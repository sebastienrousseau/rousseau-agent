package agent

// LedgerEntry is one tool call made during a turn: what ran and
// whether it reported failure. A list of these is the side-effect
// ledger a sender sees when a restart cuts their turn off.
type LedgerEntry struct {
	Tool   string
	Detail string
	// Failed is set when the tool reported an error, or when no result
	// was recorded for the call (it may or may not have completed).
	Failed bool
}

// TurnLedger lists the tool calls made since the last message the
// sender wrote: the side effects of the latest (possibly interrupted)
// turn, in order. Tool results the agent appends are user-role
// messages too, so the turn starts at the last user message that holds
// anything other than tool results.
func TurnLedger(s *Session) []LedgerEntry {
	if s == nil {
		return nil
	}
	var out []LedgerEntry
	index := map[string]int{}
	for _, m := range s.Messages[lastSenderMessage(s.Messages)+1:] {
		for _, c := range m.Content {
			switch {
			case c.Kind == ContentToolUse && c.ToolUse != nil:
				index[c.ToolUse.ID] = len(out)
				out = append(out, LedgerEntry{
					Tool:   c.ToolUse.Name,
					Detail: summarizeToolInput(c.ToolUse.Name, c.ToolUse.Input),
					Failed: true, // until a successful result is seen
				})
			case c.Kind == ContentToolResult && c.ToolResult != nil:
				if i, ok := index[c.ToolResult.ToolUseID]; ok {
					out[i].Failed = c.ToolResult.IsError
				}
			}
		}
	}
	return out
}

// lastSenderMessage returns the index of the last user message that is
// not purely tool results, or -1.
func lastSenderMessage(msgs []Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleUser && !onlyToolResults(msgs[i]) {
			return i
		}
	}
	return -1
}

func onlyToolResults(m Message) bool {
	for _, c := range m.Content {
		if c.Kind != ContentToolResult {
			return false
		}
	}
	return len(m.Content) > 0
}
