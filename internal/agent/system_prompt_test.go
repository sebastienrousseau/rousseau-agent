package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

type stubSkills struct{ appendix string }

func (s stubSkills) SystemAppendix(_ *Session) string { return s.appendix }

type stubRecall struct{ appendix string }

func (r stubRecall) SystemAppendix(context.Context, *Session) string { return r.appendix }

func newAgent(opts Options) *Agent {
	return New(&stubProvider{}, tools.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
}

func TestSystemPrompt_BaseOnly(t *testing.T) {
	a := newAgent(Options{SystemPrompt: "base"})
	assert.Equal(t, "base", a.systemPrompt(context.Background(), NewSession("x")))
}

func TestSystemPrompt_WithSkillsOnly(t *testing.T) {
	a := newAgent(Options{
		SystemPrompt:   "base",
		SkillsProvider: stubSkills{appendix: "skills-text"},
	})
	got := a.systemPrompt(context.Background(), NewSession("x"))
	assert.Equal(t, "base\n\nskills-text", got)
}

func TestSystemPrompt_WithRecallOnly(t *testing.T) {
	a := newAgent(Options{
		SystemPrompt:   "base",
		RecallProvider: stubRecall{appendix: "recall-text"},
	})
	got := a.systemPrompt(context.Background(), NewSession("x"))
	assert.Equal(t, "base\n\nrecall-text", got)
}

func TestSystemPrompt_WithBoth(t *testing.T) {
	a := newAgent(Options{
		SystemPrompt:   "base",
		SkillsProvider: stubSkills{appendix: "skills"},
		RecallProvider: stubRecall{appendix: "recall"},
	})
	got := a.systemPrompt(context.Background(), NewSession("x"))
	assert.Equal(t, "base\n\nskills\n\nrecall", got)
}

func TestSystemPrompt_AllEmpty(t *testing.T) {
	a := newAgent(Options{})
	assert.Empty(t, a.systemPrompt(context.Background(), NewSession("x")))
}

func TestSystemPrompt_OnlySkillsNoBase(t *testing.T) {
	a := newAgent(Options{SkillsProvider: stubSkills{appendix: "only-skills"}})
	assert.Equal(t, "only-skills", a.systemPrompt(context.Background(), NewSession("x")))
}
