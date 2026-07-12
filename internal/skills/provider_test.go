package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func TestFromDir_MissingIsNoop(t *testing.T) {
	p, err := FromDir("/nonexistent")
	require.NoError(t, err)
	assert.Empty(t, p.Skills())
}

func TestProvider_NilReceiverIsSafe(t *testing.T) {
	var p *Provider
	assert.Empty(t, p.SystemAppendix(agent.NewSession("x")))
	assert.Nil(t, p.Skills())
}

func TestProvider_EmptySkillsProducesNoAppendix(t *testing.T) {
	p := NewProvider(nil)
	sess := agent.NewSession("x")
	sess.Append(agent.NewUserText("hello"))
	assert.Empty(t, p.SystemAppendix(sess))
}

func TestProvider_ActivatesOnKeyword(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rebase.md"), []byte(`---
name: git-rebase
description: Guide the user through a rebase safely.
triggers: [rebase, squash]
---
Never force-push to main.`), 0o644))

	p, err := FromDir(dir)
	require.NoError(t, err)

	sess := agent.NewSession("x")
	sess.Append(agent.NewUserText("help me squash these commits"))

	got := p.SystemAppendix(sess)
	assert.Contains(t, got, "Active skills")
	assert.Contains(t, got, "git-rebase")
	assert.Contains(t, got, "Never force-push")
}

func TestProvider_NoUserMessageProducesNothing(t *testing.T) {
	p := NewProvider([]Skill{
		{Name: "kubernetes", Triggers: []string{"kubectl"}, Body: "K8s guidance."},
	})
	sess := agent.NewSession("x")
	sess.Append(agent.NewAssistantText("hi"))
	assert.Empty(t, p.SystemAppendix(sess))
}

func TestProvider_UsesLatestUserMessage(t *testing.T) {
	p := NewProvider([]Skill{
		{Name: "git", Triggers: []string{"rebase"}, Body: "git."},
		{Name: "k8s", Triggers: []string{"kubectl"}, Body: "k8s."},
	})
	sess := agent.NewSession("x")
	sess.Append(agent.NewUserText("first: rebase"))
	sess.Append(agent.NewAssistantText("ok"))
	sess.Append(agent.NewUserText("second: kubectl"))
	got := p.SystemAppendix(sess)
	assert.Contains(t, got, "k8s")
	assert.NotContains(t, got, "git.")
}

func TestLastUserText_SkipsEmptyUser(t *testing.T) {
	sess := agent.NewSession("x")
	sess.Append(agent.Message{Role: agent.RoleUser, Content: []agent.Content{{Kind: agent.ContentToolResult}}})
	_, ok := lastUserText(sess)
	assert.False(t, ok)
}
