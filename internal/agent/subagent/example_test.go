package subagent_test

import (
	"context"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
)

// ExampleSpawn shows the intended usage: three independent
// sub-agents run in parallel against a shared parent session.
func ExampleSpawn() {
	provider := &exampleProvider{}
	session := agent.NewSession("triage")
	tasks := []subagent.Task{
		{Prompt: "check inbox"},
		{Prompt: "check calendar"},
		{Prompt: "check github"},
	}
	results, _ := subagent.Spawn(context.Background(), session, provider, tasks, subagent.Policy{MaxConcurrent: 3}, nil) //nolint:errcheck // example
	fmt.Println(len(results), "tasks completed")
	// Output: 3 tasks completed
}

type exampleProvider struct{}

func (*exampleProvider) Name() string { return "example" }
func (*exampleProvider) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{
		Message:    agent.NewAssistantText("done"),
		StopReason: agent.StopEndTurn,
		Usage:      agent.Usage{InputTokens: 10, OutputTokens: 3},
	}, nil
}
