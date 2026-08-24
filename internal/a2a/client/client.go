// Package client is the A2A client-side surface — used by a rousseau
// daemon to dispatch tasks to a peer agent and receive updates via
// Server-Sent Events.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// Config configures a per-peer A2A client.
type Config struct {
	// Name identifies the peer in logs and metrics.
	Name string
	// Endpoint is the peer's A2A URL (e.g. https://peer.example.com).
	// Endpoint should NOT include the trailing "/tasks" or the
	// "/.well-known/..." suffix — the client appends the paths.
	Endpoint string
	// AuthHeader is sent verbatim on every request (typically
	// "Bearer <token>"). Empty disables auth.
	AuthHeader string
	// Timeout bounds a single non-streaming request (FetchCard, cancel,
	// POST /tasks). Zero uses 60s. The event stream ignores this — it
	// stays open for the task's lifetime.
	Timeout time.Duration
	// HTTPClient overrides the transport. Zero uses http.DefaultClient.
	HTTPClient *http.Client
}

// Client is one A2A peer connection. Instantiate one per remote agent
// the daemon coordinates with.
type Client struct {
	cfg    Config
	base   *url.URL
	client *http.Client
}

// New constructs a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("a2a/client: Endpoint is required")
	}
	if cfg.Name == "" {
		return nil, errors.New("a2a/client: Name is required")
	}
	base, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("a2a/client: invalid Endpoint %q: %w", cfg.Endpoint, err)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{cfg: cfg, base: base, client: hc}, nil
}

// Name returns the configured peer name.
func (c *Client) Name() string { return c.cfg.Name }

// FetchCard resolves the peer's capability card via
// GET /.well-known/agent-capabilities.
func (c *Client) FetchCard(ctx context.Context) (a2a.CapabilityCard, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/.well-known/agent-capabilities", nil)
	if err != nil {
		return a2a.CapabilityCard{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return a2a.CapabilityCard{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return a2a.CapabilityCard{}, unexpectedStatus(resp)
	}
	var card a2a.CapabilityCard
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&card); err != nil {
		return a2a.CapabilityCard{}, fmt.Errorf("a2a/client: decode card: %w", err)
	}
	return card, nil
}

// SubmitTask posts a task to the peer and returns a channel of updates.
// The channel closes once a terminal Status arrives or ctx cancels.
//
// The task ID is picked by the server when task.TaskID is empty; the
// canonical ID is included in every TaskUpdate on the channel.
func (c *Client) SubmitTask(ctx context.Context, task a2a.Task) (<-chan a2a.TaskUpdate, error) {
	if task.Prompt == "" && task.SkillName == "" {
		return nil, errors.New("a2a/client: task requires prompt or skill_name")
	}

	// 1. POST /tasks to get the assigned task_id.
	postCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	payload, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(postCtx, http.MethodPost, "/tasks", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusAccepted {
		defer func() { _ = resp.Body.Close() }()
		return nil, unexpectedStatus(resp)
	}
	var ack struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&ack); err != nil {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("a2a/client: decode task ack: %w", err)
	}
	_ = resp.Body.Close()
	if ack.TaskID == "" {
		return nil, errors.New("a2a/client: server did not return task_id")
	}

	// 2. Open the SSE stream — uses ctx (not the postCtx) so it can
	//    stay open for the whole task lifetime.
	streamReq, err := c.newRequest(ctx, http.MethodGet, "/tasks/"+ack.TaskID+"/events", nil)
	if err != nil {
		return nil, err
	}
	streamReq.Header.Set("Accept", "text/event-stream")
	streamResp, err := c.client.Do(streamReq)
	if err != nil {
		return nil, err
	}
	if streamResp.StatusCode != http.StatusOK {
		defer func() { _ = streamResp.Body.Close() }()
		return nil, unexpectedStatus(streamResp)
	}

	// 3. Fan updates out into a channel until close/ctx-cancel.
	out := make(chan a2a.TaskUpdate, 16)
	go func() {
		defer close(out)
		defer func() { _ = streamResp.Body.Close() }()
		scanner := bufio.NewScanner(streamResp.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var upd a2a.TaskUpdate
			if err := json.Unmarshal([]byte(line[len("data: "):]), &upd); err != nil {
				continue // skip malformed frames; server is authoritative
			}
			select {
			case out <- upd:
			case <-ctx.Done():
				return
			}
			if isTerminal(upd.Status) {
				return
			}
		}
	}()

	return out, nil
}

// Cancel asks the peer to abort a running task. Returns nil once the
// server accepts the cancel — the SSE stream will drain to
// Status=cancelled shortly after.
func (c *Client) Cancel(ctx context.Context, taskID string) error {
	if taskID == "" {
		return errors.New("a2a/client: taskID is required")
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodPost, "/tasks/"+taskID+"/cancel", nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return unexpectedStatus(resp)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	rel, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.ResolveReference(rel).String(), body)
	if err != nil {
		return nil, err
	}
	if c.cfg.AuthHeader != "" {
		req.Header.Set("Authorization", c.cfg.AuthHeader)
	}
	return req, nil
}

func unexpectedStatus(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12)) //nolint:errcheck // best-effort read
	return fmt.Errorf("a2a/client: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func isTerminal(s a2a.TaskStatus) bool {
	switch s {
	case a2a.TaskStatusCompleted, a2a.TaskStatusFailed, a2a.TaskStatusCancelled:
		return true
	default:
		return false
	}
}
