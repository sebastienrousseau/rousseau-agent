package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/recall"
)

// fakeVectorRetriever satisfies vectorRetriever with scripted output.
type fakeVectorRetriever struct {
	hits  []recall.Hit
	err   error
	query string
	k     int
}

func (f *fakeVectorRetriever) Recall(_ context.Context, query string, k int) ([]recall.Hit, error) {
	f.query = query
	f.k = k
	return f.hits, f.err
}

func newSession(msgs ...agent.Message) *agent.Session {
	s := agent.NewSession("t")
	s.Messages = msgs
	return s
}

func userMsg(text string) agent.Message {
	return agent.Message{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Kind: agent.ContentText, Text: text}},
	}
}

func TestVectorRecall_NilReceiverIsSafe(t *testing.T) {
	var v *vectorRecall
	got := v.SystemAppendix(context.Background(), newSession(userMsg("hi")))
	assert.Empty(t, got)
}

func TestVectorRecall_NilRetrieverReturnsEmpty(t *testing.T) {
	v := &vectorRecall{}
	got := v.SystemAppendix(context.Background(), newSession(userMsg("hi")))
	assert.Empty(t, got)
}

func TestVectorRecall_NoUserMessageReturnsEmpty(t *testing.T) {
	fake := &fakeVectorRetriever{}
	v := &vectorRecall{retriever: fake}
	got := v.SystemAppendix(context.Background(), newSession())
	assert.Empty(t, got)
	assert.Empty(t, fake.query, "retriever must not fire when there is no user text")
}

func TestVectorRecall_HappyPathComposesAppendix(t *testing.T) {
	fake := &fakeVectorRetriever{hits: []recall.Hit{
		{Row: recall.Row{SessionID: "old-1", Text: "we talked about kafka rebalances"}, Score: 0.9},
		{Row: recall.Row{SessionID: "old-2", Text: "the postgres migration hit a rollback"}, Score: 0.7},
	}}
	v := &vectorRecall{retriever: fake}
	sess := newSession(userMsg("how did we handle kafka?"))

	got := v.SystemAppendix(context.Background(), sess)
	assert.Contains(t, got, "# Related prior sessions")
	assert.Contains(t, got, "session old-1")
	assert.Contains(t, got, "we talked about kafka rebalances")
	assert.Contains(t, got, "session old-2")
	assert.Equal(t, "how did we handle kafka?", fake.query)
	assert.Equal(t, 3, fake.k, "empty Limit defaults to 3")
}

func TestVectorRecall_LimitOverridesDefault(t *testing.T) {
	fake := &fakeVectorRetriever{hits: []recall.Hit{{Row: recall.Row{SessionID: "x", Text: "y"}}}}
	v := &vectorRecall{retriever: fake, Limit: 7}
	_ = v.SystemAppendix(context.Background(), newSession(userMsg("hi")))
	assert.Equal(t, 7, fake.k)
}

func TestVectorRecall_SkipSessionIDFiltersCurrent(t *testing.T) {
	fake := &fakeVectorRetriever{hits: []recall.Hit{
		{Row: recall.Row{SessionID: "keep", Text: "wanted"}},
		{Row: recall.Row{SessionID: "drop-me", Text: "current session's echo"}},
	}}
	v := &vectorRecall{
		retriever:     fake,
		SkipSessionID: func(*agent.Session) string { return "drop-me" },
	}
	got := v.SystemAppendix(context.Background(), newSession(userMsg("hi")))
	assert.Contains(t, got, "wanted")
	assert.NotContains(t, got, "current session's echo")
}

func TestVectorRecall_AllHitsFilteredReturnsEmpty(t *testing.T) {
	fake := &fakeVectorRetriever{hits: []recall.Hit{
		{Row: recall.Row{SessionID: "only-current", Text: "echo"}},
	}}
	v := &vectorRecall{
		retriever:     fake,
		SkipSessionID: func(*agent.Session) string { return "only-current" },
	}
	got := v.SystemAppendix(context.Background(), newSession(userMsg("hi")))
	assert.Empty(t, got, "no surviving hits → no appendix")
}

func TestVectorRecall_RetrieverErrorReturnsEmpty(t *testing.T) {
	fake := &fakeVectorRetriever{err: errors.New("boom")}
	v := &vectorRecall{retriever: fake}
	got := v.SystemAppendix(context.Background(), newSession(userMsg("hi")))
	assert.Empty(t, got, "retriever errors are swallowed — recall is best-effort")
}

func TestVectorRecall_TitleForHitOverridesDefault(t *testing.T) {
	fake := &fakeVectorRetriever{hits: []recall.Hit{
		{Row: recall.Row{SessionID: "abc", Text: "body"}, Score: 0.9},
	}}
	v := &vectorRecall{
		retriever:   fake,
		TitleForHit: func(h recall.Hit) string { return "Score " + fmtFloat(h.Score) },
	}
	got := v.SystemAppendix(context.Background(), newSession(userMsg("hi")))
	assert.Contains(t, got, "## Score 0.90")
}

// TestVectorRecall_TitleForHitReturningEmptyFallsBackToDefault
// covers the branch where titleFor is non-nil but returns "" — must
// fall through to the "session <id>" default rather than emit an
// empty heading.
func TestVectorRecall_TitleForHitReturningEmptyFallsBackToDefault(t *testing.T) {
	fake := &fakeVectorRetriever{hits: []recall.Hit{
		{Row: recall.Row{SessionID: "abc", Text: "body"}},
	}}
	v := &vectorRecall{
		retriever:   fake,
		TitleForHit: func(recall.Hit) string { return "" },
	}
	got := v.SystemAppendix(context.Background(), newSession(userMsg("hi")))
	assert.Contains(t, got, "## session abc")
}

func TestLastUserText_MultipleContentBlocksJoined(t *testing.T) {
	sess := newSession(agent.Message{
		Role: agent.RoleUser,
		Content: []agent.Content{
			{Kind: agent.ContentText, Text: "line 1"},
			{Kind: agent.ContentText, Text: "line 2"},
			{Kind: agent.ContentToolUse}, // ignored
		},
	})
	got, ok := lastUserText(sess)
	require.True(t, ok)
	assert.Equal(t, "line 1\nline 2", got)
}

func TestLastUserText_WalksBackwardsPastAssistant(t *testing.T) {
	sess := newSession(
		userMsg("earlier user"),
		agent.NewAssistantText("assistant reply"),
		userMsg(""), // empty user — walked past
		agent.NewAssistantText("another reply"),
	)
	got, ok := lastUserText(sess)
	require.True(t, ok)
	assert.Equal(t, "earlier user", got)
}

func TestLastUserText_NilSession(t *testing.T) {
	_, ok := lastUserText(nil)
	assert.False(t, ok)
}

func TestLastUserText_NoUserMessagesReturnsFalse(t *testing.T) {
	sess := newSession(agent.NewAssistantText("solo"))
	_, ok := lastUserText(sess)
	assert.False(t, ok)
}

// fmtFloat exists so the TitleForHit test can assert against a
// stable string without importing a formatting helper at test scope.
func fmtFloat(f float32) string {
	// Two-decimal fixed format, no scientific notation. Enough
	// precision for the test scores and matches how a user would
	// hand-format a score in a title.
	whole := int(f * 100)
	return "0." + zeropad(whole)
}

func zeropad(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

