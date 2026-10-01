package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingStructured struct {
	stubCompProvider
	out    json.RawMessage
	schema map[string]any
	req    Request
}

func (c *capturingStructured) CompleteStructured(_ context.Context, req Request, schema map[string]any) (json.RawMessage, error) {
	c.req, c.schema = req, schema
	return c.out, nil
}

func TestDecide_YesNo(t *testing.T) {
	p := &capturingStructured{out: json.RawMessage(`{"answer":"yes","confidence":0.92,"reason":"deletes the root filesystem"}`)}
	got, err := Decide(context.Background(), p, DecisionRequest{
		Kind: DecisionYesNo, Question: "Is this destructive?", Context: `bash {"command":"rm -rf /"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "yes", got.Answer)
	assert.InDelta(t, 0.92, got.Confidence, 1e-9)
	assert.Equal(t, "deletes the root filesystem", got.Reason)
	props := p.schema["properties"].(map[string]any)
	assert.Equal(t, []any{"yes", "no"}, props["answer"].(map[string]any)["enum"])
	assert.Contains(t, p.req.Messages[0].Content[0].Text, "rm -rf /", "context reaches the model")
}

func TestDecide_ChoiceAndScoreConstrainTheAnswer(t *testing.T) {
	p := &capturingStructured{out: json.RawMessage(`{"answer":"billing","confidence":0.7,"reason":"invoice"}`)}
	got, err := Decide(context.Background(), p, DecisionRequest{
		Kind: DecisionChoice, Question: "Route this ticket", Options: []string{"billing", "tech", "sales"},
	})
	require.NoError(t, err)
	assert.Equal(t, "billing", got.Answer)
	assert.Equal(t, []any{"billing", "tech", "sales"},
		p.schema["properties"].(map[string]any)["answer"].(map[string]any)["enum"])

	p.out = json.RawMessage(`{"answer":"high","confidence":0.6,"reason":"outage"}`)
	got, err = Decide(context.Background(), p, DecisionRequest{
		Kind: DecisionScore, Question: "Urgency?", Options: []string{"low", "medium", "high"},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, got.Index, "score answers carry their position on the scale")
}

func TestDecide_RejectsBadRequestsAndOutOfRangeConfidence(t *testing.T) {
	_, err := Decide(context.Background(), &capturingStructured{}, DecisionRequest{Kind: DecisionChoice, Question: "x"})
	assert.ErrorContains(t, err, "options")
	_, err = Decide(context.Background(), &capturingStructured{}, DecisionRequest{Kind: "vibes", Question: "x"})
	assert.ErrorContains(t, err, "kind")

	p := &capturingStructured{out: json.RawMessage(`{"answer":"no","confidence":1.7,"reason":"r"}`)}
	_, err = Decide(context.Background(), p, DecisionRequest{Kind: DecisionYesNo, Question: "x"})
	assert.ErrorContains(t, err, "confidence")
}
