package router

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// namedStub is a minimal child provider for the in-package tests.
type namedStub struct{ name string }

func (s *namedStub) Name() string { return s.name }
func (s *namedStub) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{Message: agent.NewAssistantText(s.name), StopReason: agent.StopEndTurn}, nil
}

// intentJudge answers the classification with a fixed label.
type intentJudge struct {
	namedStub
	label   string
	err     error
	options []any
	calls   int
}

func (j *intentJudge) CompleteStructured(_ context.Context, _ agent.Request, schema map[string]any) (json.RawMessage, error) {
	j.calls++
	j.options = schema["properties"].(map[string]any)["answer"].(map[string]any)["enum"].([]any)
	if j.err != nil {
		return nil, j.err
	}
	b, _ := json.Marshal(map[string]any{"answer": j.label, "confidence": 0.9, "reason": "r"}) //nolint:errcheck // fixture
	return b, nil
}

func newIntentRouter(t *testing.T, judge *intentJudge) *Router {
	t.Helper()
	r, err := New(Options{
		Default: "big",
		Rules: []Rule{
			{Name: "chit-chat", Intents: []string{"smalltalk"}, Use: "small"},
			{Name: "code", Intents: []string{"coding", "debugging"}, Use: "big"},
		},
		Providers:  map[string]agent.Provider{"big": &namedStub{name: "big"}, "small": &namedStub{name: "small"}},
		Classifier: judge,
	})
	require.NoError(t, err)
	return r
}

// TestIntentRouting pins semantic routing: the last user message is
// classified once into the labels the rules name (plus "other"), and
// the first rule whose intents contain the label wins.
func TestIntentRouting(t *testing.T) {
	judge := &intentJudge{label: "smalltalk"}
	r := newIntentRouter(t, judge)
	req := agent.Request{Messages: []agent.Message{agent.NewUserText("hey, how's it going?")}}

	key, rule := r.selectChild(context.Background(), req)
	assert.Equal(t, "small", key)
	assert.Equal(t, "chit-chat", rule)
	assert.Equal(t, 1, judge.calls, "classified once per request")
	assert.ElementsMatch(t, []any{"smalltalk", "coding", "debugging", "other"}, judge.options)

	judge.label = "other"
	key, rule = r.selectChild(context.Background(), req)
	assert.Equal(t, "big", key)
	assert.Equal(t, "default", rule)
}

func TestIntentRouting_ClassifierFailureFallsBackToOther(t *testing.T) {
	r := newIntentRouter(t, &intentJudge{err: errors.New("down")})
	key, rule := r.selectChild(context.Background(), agent.Request{Messages: []agent.Message{agent.NewUserText("hi")}})
	assert.Equal(t, "big", key)
	assert.Equal(t, "default", rule)
}

func TestIntentRouting_RequiresClassifier(t *testing.T) {
	_, err := New(Options{
		Default:   "big",
		Rules:     []Rule{{Intents: []string{"smalltalk"}, Use: "big"}},
		Providers: map[string]agent.Provider{"big": &namedStub{name: "big"}},
	})
	assert.ErrorContains(t, err, "classifier")
}

func TestIntentRouting_NoIntentRulesNeverClassify(t *testing.T) {
	judge := &intentJudge{label: "x"}
	r, err := New(Options{
		Default:    "big",
		Rules:      []Rule{{MessageLenMax: 5, Use: "small"}},
		Providers:  map[string]agent.Provider{"big": &namedStub{name: "big"}, "small": &namedStub{name: "small"}},
		Classifier: judge,
	})
	require.NoError(t, err)
	_, _ = r.selectChild(context.Background(), agent.Request{Messages: []agent.Message{agent.NewUserText("hi")}})
	assert.Zero(t, judge.calls)
}
