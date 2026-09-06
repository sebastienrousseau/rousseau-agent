package a2a

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskState_IsTerminal(t *testing.T) {
	t.Parallel()
	terminal := []TaskState{TaskStateCompleted, TaskStateFailed, TaskStateCanceled, TaskStateRejected}
	notTerminal := []TaskState{TaskStateSubmitted, TaskStateWorking, TaskStateInputRequired, TaskStateAuthRequired, TaskStateUnspecified, TaskState("bogus")}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("expected %q terminal", s)
		}
	}
	for _, s := range notTerminal {
		if s.IsTerminal() {
			t.Errorf("expected %q non-terminal", s)
		}
	}
}

func TestFromLegacyStatus_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in         TaskStatus
		want       TaskState
		legacyBack TaskStatus
	}{
		{TaskStatusRunning, TaskStateWorking, TaskStatusRunning},
		{TaskStatusCompleted, TaskStateCompleted, TaskStatusCompleted},
		{TaskStatusFailed, TaskStateFailed, TaskStatusFailed},
		{TaskStatusCancelled, TaskStateCanceled, TaskStatusCancelled},
	}
	for _, tc := range cases {
		got := FromLegacyStatus(tc.in)
		if got != tc.want {
			t.Errorf("FromLegacyStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if back := got.ToLegacyStatus(); back != tc.legacyBack {
			t.Errorf("%q → %q → %q, want %q", tc.in, got, back, tc.legacyBack)
		}
	}
	if got := FromLegacyStatus(TaskStatus("bogus")); got != TaskStateUnspecified {
		t.Errorf("unknown legacy status: got %q, want TASK_STATE_UNSPECIFIED", got)
	}
}

func TestToLegacyStatus_CoveringNewStates(t *testing.T) {
	t.Parallel()
	// v1 introduces states v0 didn't have — check they collapse
	// sensibly.
	if got := TaskStateInputRequired.ToLegacyStatus(); got != TaskStatusRunning {
		t.Errorf("input_required legacy = %q, want running", got)
	}
	if got := TaskStateAuthRequired.ToLegacyStatus(); got != TaskStatusRunning {
		t.Errorf("auth_required legacy = %q, want running", got)
	}
	if got := TaskStateSubmitted.ToLegacyStatus(); got != TaskStatusRunning {
		t.Errorf("submitted legacy = %q, want running", got)
	}
	if got := TaskStateRejected.ToLegacyStatus(); got != TaskStatusFailed {
		t.Errorf("rejected legacy = %q, want failed", got)
	}
	if got := TaskStateUnspecified.ToLegacyStatus(); got != TaskStatusRunning {
		t.Errorf("unspecified legacy = %q, want running", got)
	}
}

func TestUpgradeCard_PopulatesSpecShape(t *testing.T) {
	t.Parallel()
	legacy := CapabilityCard{
		AgentID: "peer-1",
		Name:    "peer-1",
		Version: "v0.0.4",
		Skills: []SkillDescriptor{
			{Name: "echo", Description: "returns the prompt", Tags: []string{"demo"}},
			{Name: "review"},
		},
		SupportsStreaming: true,
	}
	card := UpgradeCard(legacy)
	if card.Name != "peer-1" || card.Version != "v0.0.4" {
		t.Fatalf("card top-level not populated: %+v", card)
	}
	if !card.Capabilities.Streaming {
		t.Errorf("streaming capability lost in upgrade")
	}
	if card.ProtocolVersion != SpecVersion {
		t.Errorf("protocolVersion = %q, want %q", card.ProtocolVersion, SpecVersion)
	}
	if len(card.Skills) != 2 {
		t.Fatalf("skills count = %d, want 2", len(card.Skills))
	}
	if card.Skills[0].ID != "echo" || card.Skills[0].Name != "echo" {
		t.Errorf("skill[0] = %+v", card.Skills[0])
	}
	if got := strings.Join(card.DefaultInputModes, ","); got != "text/plain" {
		t.Errorf("defaultInputModes = %q, want text/plain", got)
	}
}

func TestTextMessage_ShapeIsSpecConformant(t *testing.T) {
	t.Parallel()
	m := TextMessage("hello world")
	if m.Role != RoleUser {
		t.Errorf("role = %q, want user", m.Role)
	}
	if len(m.Parts) != 1 || m.Parts[0].Kind != PartKindText || m.Parts[0].Text != "hello world" {
		t.Errorf("parts unexpected: %+v", m.Parts)
	}
	if m.MessageID == "" || !strings.HasPrefix(m.MessageID, "msg-") {
		t.Errorf("messageId not populated: %q", m.MessageID)
	}
}

func TestPromptFromMessage_ExtractsText(t *testing.T) {
	t.Parallel()
	if got := PromptFromMessage(TextMessage("solo")); got != "solo" {
		t.Errorf("single text = %q, want solo", got)
	}
	multi := Message{
		Parts: []Part{
			{Kind: PartKindText, Text: "one"},
			{Kind: PartKindFile, File: &FilePart{URI: "artifact://x/y"}},
			{Kind: PartKindText, Text: "two"},
		},
	}
	if got := PromptFromMessage(multi); got != "one\ntwo" {
		t.Errorf("multi = %q, want one\\ntwo", got)
	}
	if got := PromptFromMessage(Message{}); got != "" {
		t.Errorf("empty parts = %q, want empty string", got)
	}
}

func TestMessage_JSONShape_MatchesSpec(t *testing.T) {
	t.Parallel()
	// Peers implementing the spec expect these exact field names —
	// this test freezes the wire shape so a rename here becomes a
	// deliberate spec-conformance decision, not a silent break.
	m := Message{
		MessageID: "msg-1",
		ContextID: "ctx-1",
		TaskID:    "task-1",
		Role:      RoleAgent,
		Parts: []Part{
			{Kind: PartKindText, Text: "hi"},
			{Kind: PartKindData, Data: json.RawMessage(`{"k":"v"}`)},
		},
	}
	blob, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(blob)
	for _, want := range []string{
		`"messageId":"msg-1"`,
		`"contextId":"ctx-1"`,
		`"taskId":"task-1"`,
		`"role":"agent"`,
		`"kind":"text"`,
		`"kind":"data"`,
		`"data":{"k":"v"}`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled message missing %q: %s", want, s)
		}
	}
}

func TestSpecTask_JSONShape_MatchesSpec(t *testing.T) {
	t.Parallel()
	task := SpecTask{
		ID:        "task-1",
		ContextID: "ctx-1",
		Status: TaskStatus1{
			State: TaskStateWorking,
		},
	}
	blob, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	s := string(blob)
	for _, want := range []string{
		`"id":"task-1"`,
		`"contextId":"ctx-1"`,
		`"state":"TASK_STATE_WORKING"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled task missing %q: %s", want, s)
		}
	}
}

func TestAgentCard_JSONShape_MatchesSpec(t *testing.T) {
	t.Parallel()
	card := UpgradeCard(CapabilityCard{Name: "peer", Version: "v0.0.4"})
	blob, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	s := string(blob)
	for _, want := range []string{
		`"name":"peer"`,
		`"version":"v0.0.4"`,
		`"protocolVersion":"1.0"`,
		`"capabilities"`,
		`"defaultInputModes":["text/plain"]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled card missing %q: %s", want, s)
		}
	}
}

func TestNewMessageID_MonotonicUnique(t *testing.T) {
	t.Parallel()
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		id := newMessageID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate messageId at i=%d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}
