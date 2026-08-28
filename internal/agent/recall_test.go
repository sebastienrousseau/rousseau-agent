package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubSearcher struct {
	hits []SearchHit
	err  error
	seen string
}

func (s *stubSearcher) Search(_ context.Context, q string, _ int) ([]SearchHit, error) {
	s.seen = q
	return s.hits, s.err
}

func TestFTSRecall_NilReceiverIsSafe(t *testing.T) {
	var r *FTSRecall
	assert.Empty(t, r.SystemAppendix(context.Background(), NewSession("x")))
}

func TestFTSRecall_NilSearcherIsSafe(t *testing.T) {
	r := &FTSRecall{}
	assert.Empty(t, r.SystemAppendix(context.Background(), NewSession("x")))
}

func TestFTSRecall_NoUserMessageIsNoop(t *testing.T) {
	s := &stubSearcher{}
	r := &FTSRecall{Searcher: s}
	assert.Empty(t, r.SystemAppendix(context.Background(), NewSession("x")))
	assert.Empty(t, s.seen, "searcher should not be called with no user text")
}

func TestFTSRecall_SkipsShortKeywords(t *testing.T) {
	s := &stubSearcher{hits: []SearchHit{{SessionID: "past", Title: "old", Snippet: "content"}}}
	r := &FTSRecall{Searcher: s, MinKeywordLen: 5}
	sess := NewSession("x")
	sess.Append(NewUserText("hi ok yo")) // all short
	got := r.SystemAppendix(context.Background(), sess)
	assert.Empty(t, got)
}

func TestFTSRecall_ComposesAppendix(t *testing.T) {
	s := &stubSearcher{hits: []SearchHit{
		{SessionID: "past-1", Title: "old kubernetes chat", Snippet: "we discussed pod affinity"},
	}}
	r := &FTSRecall{Searcher: s}
	sess := NewSession("x")
	sess.Append(NewUserText("more kubernetes questions today"))
	got := r.SystemAppendix(context.Background(), sess)
	assert.Contains(t, got, "Related prior sessions")
	assert.Contains(t, got, "old kubernetes chat")
	assert.Contains(t, got, "pod affinity")
	assert.Contains(t, s.seen, "kubernetes")
	assert.Contains(t, s.seen, "OR")
}

func TestFTSRecall_SkipsOwnSession(t *testing.T) {
	sess := NewSession("x")
	sess.Append(NewUserText("kubernetes today please"))
	s := &stubSearcher{hits: []SearchHit{
		{SessionID: sess.ID, Title: "self", Snippet: "self hit"},
		{SessionID: "other", Title: "other", Snippet: "other hit"},
	}}
	r := &FTSRecall{
		Searcher:      s,
		SkipSessionID: func(sess *Session) string { return sess.ID },
	}
	got := r.SystemAppendix(context.Background(), sess)
	assert.Contains(t, got, "other")
	assert.NotContains(t, got, "self hit")
}

func TestFTSRecall_SearchErrorIsSilent(t *testing.T) {
	s := &stubSearcher{err: errors.New("db down")}
	r := &FTSRecall{Searcher: s}
	sess := NewSession("x")
	sess.Append(NewUserText("kubernetes matters"))
	assert.Empty(t, r.SystemAppendix(context.Background(), sess))
}

func TestKeywords_JoinsWithOR(t *testing.T) {
	got := keywords("please help debug kubernetes pod", 4)
	assert.Contains(t, got, "help")
	assert.Contains(t, got, "debug")
	assert.Contains(t, got, "kubernetes")
	assert.Contains(t, got, "OR")
}

func TestKeywords_StripsPunctuation(t *testing.T) {
	got := keywords(`"kubernetes." commits!`, 4)
	assert.Contains(t, got, "kubernetes")
	assert.Contains(t, got, "commits")
	assert.NotContains(t, got, `"`)
}

func TestKeywords_EmptyReturnsEmpty(t *testing.T) {
	assert.Empty(t, keywords("a b c", 4))
}

func TestKeywords_CapsAtEight(t *testing.T) {
	long := "kubernetes helm istio prometheus grafana loki tempo mimir alloy"
	got := keywords(long, 4)
	// Should only include 8 terms + 7 ORs.
	assert.Equal(t, 15, len(splitWords(got)))
}

func splitWords(s string) []string {
	var out []string
	for _, w := range fieldsSep(s, ' ') {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func fieldsSep(s string, sep byte) []string {
	var out []string
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[last:i])
			last = i + 1
		}
	}
	out = append(out, s[last:])
	return out
}

func TestLastUserText_Empty(t *testing.T) {
	_, ok := lastUserText(nil)
	assert.False(t, ok)
	_, ok = lastUserText(NewSession("x"))
	assert.False(t, ok)
}

func TestLastUserText_ReturnsLatest(t *testing.T) {
	s := NewSession("x")
	s.Append(NewUserText("first"))
	s.Append(NewAssistantText("hi"))
	s.Append(NewUserText("second"))
	got, ok := lastUserText(s)
	require.True(t, ok)
	assert.Equal(t, "second", got)
}
