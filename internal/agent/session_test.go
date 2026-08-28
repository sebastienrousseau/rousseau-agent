package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSession_HasID(t *testing.T) {
	s := NewSession("hello")
	require.NotEmpty(t, s.ID)
	assert.Equal(t, "hello", s.Title)
	assert.Empty(t, s.Messages)
	assert.False(t, s.CreatedAt.IsZero())
	assert.Equal(t, s.CreatedAt, s.UpdatedAt)
}

func TestSession_AppendAdvancesUpdatedAt(t *testing.T) {
	s := NewSession("x")
	first := s.UpdatedAt

	m := NewUserText("hi")
	s.Append(m)

	require.Len(t, s.Messages, 1)
	assert.Equal(t, m, s.Messages[0])
	assert.False(t, s.UpdatedAt.Before(first))
}

func TestSession_LastEmpty(t *testing.T) {
	s := NewSession("x")
	_, ok := s.Last()
	assert.False(t, ok)
}

func TestSession_LastReturnsFinal(t *testing.T) {
	s := NewSession("x")
	s.Append(NewUserText("a"))
	s.Append(NewAssistantText("b"))

	last, ok := s.Last()
	require.True(t, ok)
	assert.Equal(t, RoleAssistant, last.Role)
	require.Len(t, last.Content, 1)
	assert.Equal(t, "b", last.Content[0].Text)
}

func TestNewUserText_IsUserRole(t *testing.T) {
	m := NewUserText("hi")
	assert.Equal(t, RoleUser, m.Role)
	require.Len(t, m.Content, 1)
	assert.Equal(t, ContentText, m.Content[0].Kind)
	assert.Equal(t, "hi", m.Content[0].Text)
}
