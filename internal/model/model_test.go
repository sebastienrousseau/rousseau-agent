package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConstructors_ShapeAndTimestamps(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)

	u := NewUserText("hi")
	assert.Equal(t, RoleUser, u.Role)
	require.Len(t, u.Content, 1)
	assert.Equal(t, ContentText, u.Content[0].Kind)
	assert.Equal(t, "hi", u.Content[0].Text)
	assert.True(t, u.CreatedAt.After(before))

	a := NewAssistantText("yo")
	assert.Equal(t, RoleAssistant, a.Role)
	assert.Equal(t, "yo", a.Content[0].Text)

	img := NewUserImage("image/png", []byte{1, 2}, "slack")
	assert.Equal(t, RoleUser, img.Role)
	require.NotNil(t, img.Content[0].Image)
	assert.Equal(t, ContentImage, img.Content[0].Kind)
	assert.Equal(t, "image/png", img.Content[0].Image.MediaType)
	assert.Equal(t, []byte{1, 2}, img.Content[0].Image.Data)
	assert.Equal(t, "slack", img.Content[0].Image.Source)
}

func TestSession_AppendAndLast(t *testing.T) {
	s := NewSession("t")
	assert.NotEmpty(t, s.ID)
	assert.Equal(t, "t", s.Title)
	assert.Equal(t, s.CreatedAt, s.UpdatedAt)

	_, ok := s.Last()
	assert.False(t, ok, "empty session has no last message")

	created := s.UpdatedAt
	time.Sleep(time.Millisecond)
	s.Append(NewUserText("a"))
	s.Append(NewAssistantText("b"))
	last, ok := s.Last()
	require.True(t, ok)
	assert.Equal(t, "b", last.Content[0].Text)
	assert.Len(t, s.Messages, 2)
	assert.True(t, s.UpdatedAt.After(created))
}

// The wire shape is stable: stores persist Messages as JSON and the
// field names are part of the schema.
func TestMessage_JSONRoundTrip(t *testing.T) {
	m := Message{Role: RoleAssistant, Content: []Content{
		{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "t1", Name: "read", Input: json.RawMessage(`{"path":"/x"}`)}},
		{Kind: ContentToolResult, ToolResult: &ToolResult{ToolUseID: "t1", Output: "ok", IsError: true}},
	}, CreatedAt: time.Unix(0, 0).UTC()}
	blob, err := json.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, string(blob), `"tool_use_id":"t1"`)
	assert.Contains(t, string(blob), `"is_error":true`)
	var back Message
	require.NoError(t, json.Unmarshal(blob, &back))
	assert.Equal(t, m, back)
}

func TestStopReasonAndStreamKinds_AreDistinct(t *testing.T) {
	assert.NotEqual(t, StopEndTurn, StopToolUse)
	assert.NotEqual(t, StopMaxTokens, StopOther)
	kinds := map[StreamEventKind]bool{StreamStart: true, StreamTextDelta: true, StreamToolUse: true, StreamResult: true, StreamOther: true}
	assert.Len(t, kinds, 5)
}
