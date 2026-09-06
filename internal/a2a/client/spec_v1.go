package client

// This file adds A2A v1.0.1-conformant client methods alongside the
// legacy v0-shorthand `FetchCard` / `SubmitTask` / `Cancel`. New
// callers should target the v1 methods (`GetAgentCard`, `SendMessage`,
// `SubscribeToTask`, `CancelTask`) so peers on the v1 spec receive
// spec-shaped requests. The v1 methods gracefully fall back to the
// legacy paths on 404 so a rousseau daemon can call peers that
// haven't upgraded yet.
//
// See docs/a2a-conformance.md for the wire diff and roadmap.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// GetAgentCard fetches the peer's v1.0-shaped AgentCard from
// `/.well-known/agent-card.json`. Falls back to the legacy
// `/.well-known/agent-capabilities` when the peer returns 404, and
// upgrades the legacy shape to a v1 [a2a.AgentCard] via
// [a2a.UpgradeCard].
func (c *Client) GetAgentCard(ctx context.Context) (a2a.AgentCard, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/.well-known/agent-card.json", nil)
	if err != nil {
		return a2a.AgentCard{}, err
	}
	req.Header.Set("Accept", a2a.ContentTypeSpec+", application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return a2a.AgentCard{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		// Legacy fallback so we can talk to pre-v1 peers.
		legacy, err := c.FetchCard(ctx)
		if err != nil {
			return a2a.AgentCard{}, fmt.Errorf("a2a/client: peer has no v1 card and legacy fallback failed: %w", err)
		}
		return a2a.UpgradeCard(legacy), nil
	}
	if resp.StatusCode != http.StatusOK {
		return a2a.AgentCard{}, unexpectedStatus(resp)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&card); err != nil {
		return a2a.AgentCard{}, fmt.Errorf("a2a/client: decode agent card: %w", err)
	}
	return card, nil
}

// SendMessage posts a spec-shaped [a2a.Message] to the peer's
// `/message:send` endpoint and returns the created [a2a.SpecTask].
// Falls back to the legacy `/tasks` submission when the peer returns
// 404 — the legacy Task is repacked into a SpecTask so callers get
// one uniform return type.
func (c *Client) SendMessage(ctx context.Context, msg a2a.Message) (a2a.SpecTask, error) {
	if len(msg.Parts) == 0 {
		return a2a.SpecTask{}, errors.New("a2a/client: message.parts is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	payload, err := json.Marshal(msg)
	if err != nil {
		return a2a.SpecTask{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/message:send", bytes.NewReader(payload))
	if err != nil {
		return a2a.SpecTask{}, err
	}
	req.Header.Set("Content-Type", a2a.ContentTypeSpec)
	req.Header.Set("Accept", a2a.ContentTypeSpec)
	resp, err := c.client.Do(req)
	if err != nil {
		return a2a.SpecTask{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return c.sendMessageLegacyFallback(ctx, msg)
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return a2a.SpecTask{}, unexpectedStatus(resp)
	}
	var task a2a.SpecTask
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&task); err != nil {
		return a2a.SpecTask{}, fmt.Errorf("a2a/client: decode task: %w", err)
	}
	if task.ID == "" {
		return a2a.SpecTask{}, errors.New("a2a/client: server did not return task.id")
	}
	return task, nil
}

// sendMessageLegacyFallback routes a v1 SendMessage call through the
// legacy `POST /tasks` shape when the peer returns 404 on the spec
// route. The v1 Message is flattened to the legacy [a2a.Task] shape;
// the response is repacked into an [a2a.SpecTask] so callers see a
// uniform return type.
func (c *Client) sendMessageLegacyFallback(ctx context.Context, msg a2a.Message) (a2a.SpecTask, error) {
	legacyTask := a2a.Task{
		TaskID:    msg.TaskID,
		FromAgent: msg.ContextID,
		Prompt:    a2a.PromptFromMessage(msg),
	}
	if legacyTask.Prompt == "" {
		return a2a.SpecTask{}, errors.New("a2a/client: legacy fallback requires a text part")
	}
	// SubmitTask returns a channel; we only want the task-id
	// acknowledgement, not the stream, so we hit the legacy submit
	// path directly here to avoid tearing an SSE connection down for
	// nothing.
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	payload, err := json.Marshal(legacyTask)
	if err != nil {
		return a2a.SpecTask{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/tasks", bytes.NewReader(payload))
	if err != nil {
		return a2a.SpecTask{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return a2a.SpecTask{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return a2a.SpecTask{}, unexpectedStatus(resp)
	}
	var ack struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&ack); err != nil {
		return a2a.SpecTask{}, fmt.Errorf("a2a/client: decode legacy ack: %w", err)
	}
	return a2a.SpecTask{
		ID:        ack.TaskID,
		ContextID: msg.ContextID,
		Status: a2a.TaskStatus1{
			State: a2a.FromLegacyStatus(a2a.TaskStatus(ack.Status)),
		},
	}, nil
}

// SubscribeToTask opens the v1.0 `GET /tasks/{id}:subscribe` SSE
// stream and returns a channel of [a2a.StreamResponse] frames. Falls
// back to the legacy `GET /tasks/{id}/events` on 404.
func (c *Client) SubscribeToTask(ctx context.Context, taskID string) (<-chan a2a.StreamResponse, error) {
	if taskID == "" {
		return nil, errors.New("a2a/client: taskID is required")
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/tasks/"+taskID+":subscribe", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return c.subscribeToTaskLegacyFallback(ctx, taskID)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, unexpectedStatus(resp)
	}
	out := make(chan a2a.StreamResponse, 16)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame a2a.StreamResponse
			if err := json.Unmarshal([]byte(line[len("data: "):]), &frame); err != nil {
				continue
			}
			select {
			case out <- frame:
			case <-ctx.Done():
				return
			}
			if frame.StatusUpdate != nil && frame.StatusUpdate.Status.State.IsTerminal() {
				return
			}
		}
	}()
	return out, nil
}

// subscribeToTaskLegacyFallback wraps the legacy SSE stream so the
// v1 callers see [a2a.StreamResponse] frames even when talking to a
// pre-v1 peer.
func (c *Client) subscribeToTaskLegacyFallback(ctx context.Context, taskID string) (<-chan a2a.StreamResponse, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/tasks/"+taskID+"/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, unexpectedStatus(resp)
	}
	out := make(chan a2a.StreamResponse, 16)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var legacy a2a.TaskUpdate
			if err := json.Unmarshal([]byte(line[len("data: "):]), &legacy); err != nil {
				continue
			}
			frame := a2a.StreamResponse{
				StatusUpdate: &a2a.TaskStatusUpdateEvent{
					TaskID: legacy.TaskID,
					Kind:   "status-update",
					Status: a2a.TaskStatus1{
						State:     a2a.FromLegacyStatus(legacy.Status),
						Timestamp: legacy.At,
					},
				},
			}
			select {
			case out <- frame:
			case <-ctx.Done():
				return
			}
			if frame.StatusUpdate.Status.State.IsTerminal() {
				return
			}
		}
	}()
	return out, nil
}

// CancelTask asks the peer to cancel a running task via the v1
// `POST /tasks/{id}:cancel` colon-verb, falling back to the legacy
// slash-suffix path on 404.
func (c *Client) CancelTask(ctx context.Context, taskID string) error {
	if taskID == "" {
		return errors.New("a2a/client: taskID is required")
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodPost, "/tasks/"+taskID+":cancel", nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	// Drain body so the connection can be reused. Errors here would
	// mean the peer already closed the socket — nothing to do about it.
	_, _ = io.Copy(io.Discard, resp.Body) //nolint:errcheck // best-effort drain
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return c.Cancel(ctx, taskID)
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("a2a/client: cancel HTTP %d", resp.StatusCode)
	}
	return nil
}
