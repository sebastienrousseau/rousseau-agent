package a2a_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
	"github.com/sebastienrousseau/rousseau-agent/internal/a2a/client"
	"github.com/sebastienrousseau/rousseau-agent/internal/a2a/server"
)

// handlerFunc lets a test wire a Handler from a closure.
type handlerFunc func(ctx context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error

func (f handlerFunc) OnTask(ctx context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
	return f(ctx, task, emit)
}

// startTestServer wires an httptest listener to a Server and returns
// the base URL + a shutdown func. Uses the mux directly for
// httptest.NewServer so the TCP loop doesn't get in the way.
func startTestServer(t *testing.T, h server.Handler, auth []string) (string, func()) {
	t.Helper()
	s, err := server.New(a2a.CapabilityCard{
		AgentID: "test-agent",
		Name:    "test-agent",
		Version: "v0.0.1",
	}, h, auth)
	require.NoError(t, err)
	ts := httptest.NewServer(newTestMux(s))
	return ts.URL, ts.Close
}

func TestSubmitTask_HappyPath(t *testing.T) {
	h := handlerFunc(func(_ context.Context, task a2a.Task, emit func(a2a.TaskUpdate)) error {
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Progress: 0.5, Message: "working"})
		emit(a2a.TaskUpdate{
			Status:     a2a.TaskStatusCompleted,
			OutputText: "hello " + task.Prompt,
		})
		return nil
	})
	base, done := startTestServer(t, h, nil)
	defer done()

	c, err := client.New(client.Config{Name: "peer", Endpoint: base})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := c.SubmitTask(ctx, a2a.Task{Prompt: "world"})
	require.NoError(t, err)

	var got []a2a.TaskUpdate
	for upd := range ch {
		got = append(got, upd)
	}
	require.GreaterOrEqual(t, len(got), 1, "must receive at least one update")
	last := got[len(got)-1]
	assert.Equal(t, a2a.TaskStatusCompleted, last.Status)
	assert.Equal(t, "hello world", last.OutputText)
}

func TestSubmitTask_HandlerFailure(t *testing.T) {
	h := handlerFunc(func(_ context.Context, _ a2a.Task, _ func(a2a.TaskUpdate)) error {
		return errors.New("boom")
	})
	base, done := startTestServer(t, h, nil)
	defer done()

	c, err := client.New(client.Config{Name: "peer", Endpoint: base})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := c.SubmitTask(ctx, a2a.Task{Prompt: "x"})
	require.NoError(t, err)

	var last a2a.TaskUpdate
	for upd := range ch {
		last = upd
	}
	assert.Equal(t, a2a.TaskStatusFailed, last.Status)
	assert.Equal(t, "boom", last.Message)
	assert.Equal(t, "handler_error", last.FailureCode)
}

func TestSubmitTask_SynthesizesCompletedWhenHandlerSilent(t *testing.T) {
	// Handler returns nil without emitting anything — server must
	// still deliver a completed marker.
	h := handlerFunc(func(_ context.Context, _ a2a.Task, _ func(a2a.TaskUpdate)) error {
		return nil
	})
	base, done := startTestServer(t, h, nil)
	defer done()

	c, err := client.New(client.Config{Name: "peer", Endpoint: base})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := c.SubmitTask(ctx, a2a.Task{Prompt: "x"})
	require.NoError(t, err)

	var last a2a.TaskUpdate
	for upd := range ch {
		last = upd
	}
	assert.Equal(t, a2a.TaskStatusCompleted, last.Status)
}

func TestCancel_TerminatesRunningTask(t *testing.T) {
	started := make(chan struct{})
	h := handlerFunc(func(ctx context.Context, _ a2a.Task, emit func(a2a.TaskUpdate)) error {
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusRunning, Message: "starting"})
		close(started)
		<-ctx.Done() // wait for cancel
		emit(a2a.TaskUpdate{Status: a2a.TaskStatusCancelled, Message: "cancelled by peer"})
		return nil
	})
	base, done := startTestServer(t, h, nil)
	defer done()

	c, err := client.New(client.Config{Name: "peer", Endpoint: base})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := c.SubmitTask(ctx, a2a.Task{Prompt: "long"})
	require.NoError(t, err)

	<-started
	// Discover the assigned task_id from the first update on the stream.
	first := <-ch
	require.NotEmpty(t, first.TaskID)
	require.NoError(t, c.Cancel(ctx, first.TaskID))

	var last a2a.TaskUpdate
	for upd := range ch {
		last = upd
	}
	assert.Equal(t, a2a.TaskStatusCancelled, last.Status)
}

func TestFetchCard_ReturnsCard(t *testing.T) {
	h := handlerFunc(func(_ context.Context, _ a2a.Task, _ func(a2a.TaskUpdate)) error { return nil })
	base, done := startTestServer(t, h, nil)
	defer done()

	c, err := client.New(client.Config{Name: "peer", Endpoint: base})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	card, err := c.FetchCard(ctx)
	require.NoError(t, err)
	assert.Equal(t, "test-agent", card.AgentID)
	assert.False(t, card.PublishedAt.IsZero(), "server should stamp PublishedAt")
}

func TestAuth_RejectsMissingAndInvalidTokens(t *testing.T) {
	h := handlerFunc(func(_ context.Context, _ a2a.Task, _ func(a2a.TaskUpdate)) error { return nil })
	base, done := startTestServer(t, h, []string{"good-token"})
	defer done()

	// No token → 401
	noAuth, err := client.New(client.Config{Name: "peer", Endpoint: base})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = noAuth.SubmitTask(ctx, a2a.Task{Prompt: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")

	// Bad token → 403
	badAuth, err := client.New(client.Config{Name: "peer", Endpoint: base, AuthHeader: "Bearer nope"})
	require.NoError(t, err)
	_, err = badAuth.SubmitTask(ctx, a2a.Task{Prompt: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")

	// Good token → succeeds
	good, err := client.New(client.Config{Name: "peer", Endpoint: base, AuthHeader: "Bearer good-token"})
	require.NoError(t, err)
	ch, err := good.SubmitTask(ctx, a2a.Task{Prompt: "x"})
	require.NoError(t, err)
	for range ch { // drain until close
	}
}

func TestClient_New_Validation(t *testing.T) {
	_, err := client.New(client.Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Endpoint")

	_, err = client.New(client.Config{Endpoint: "http://x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Name")
}

func TestServer_New_RequiresHandler(t *testing.T) {
	_, err := server.New(a2a.CapabilityCard{}, nil, nil)
	require.Error(t, err)
}
