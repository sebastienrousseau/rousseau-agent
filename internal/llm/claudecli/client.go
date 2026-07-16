// Package claudecli implements agent.Provider by shelling out to the
// installed `claude` CLI (Claude Code). It uses whatever authentication
// the user's Claude Code install already has — subscription, API key
// helper, keychain — so callers do not need to plumb ANTHROPIC_API_KEY.
//
// Because `claude` runs its own tool-use loop internally, requests
// through this provider execute tools inside the claude subprocess.
// The agent-level tools registered on the Registry are NOT invoked for
// this provider; the Response is always a single end-of-turn text
// message with claude's final answer.
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Config configures the Provider.
type Config struct {
	// Binary is the claude executable to invoke. Empty defaults to
	// "claude" (resolved on $PATH).
	Binary string
	// Model is passed to --model. Empty uses claude's default.
	Model string
	// PermissionMode is passed to --permission-mode. Empty uses
	// claude's default. For unattended daemons (e.g. WhatsApp bridge)
	// "bypassPermissions" is required, but you accept the blast radius.
	PermissionMode string
	// ExtraArgs are prepended before -p on every invocation. Useful for
	// --add-dir, --allowed-tools, --disallowed-tools, --plugin-dir …
	ExtraArgs []string
}

// runFunc executes an exec.Cmd; extracted so tests can stub it.
type runFunc func(cmd *exec.Cmd) ([]byte, error)

// SessionCache remembers which session IDs claude already has state for.
// Persistent implementations survive daemon restarts and avoid the
// "already in use" cold-start roundtrip on the first turn after
// startup. The zero-cost in-memory implementation is used by default.
type SessionCache interface {
	IsKnown(id string) bool
	Remember(id string)
}

// InMemorySessionCache is the default (non-persistent) SessionCache.
type InMemorySessionCache struct {
	mu   sync.Mutex
	seen map[string]bool
}

// NewInMemorySessionCache constructs an empty in-memory cache.
func NewInMemorySessionCache() *InMemorySessionCache {
	return &InMemorySessionCache{seen: map[string]bool{}}
}

// IsKnown satisfies SessionCache.
func (c *InMemorySessionCache) IsKnown(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[id]
}

// Remember satisfies SessionCache.
func (c *InMemorySessionCache) Remember(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[id] = true
}

// Provider is an agent.Provider backed by the `claude` CLI.
type Provider struct {
	cfg   Config
	run   runFunc
	cache SessionCache
}

// New constructs a Provider. It does not verify the binary exists;
// invocations that fail surface at Complete time.
func New(cfg Config) *Provider {
	if cfg.Binary == "" {
		cfg.Binary = "claude"
	}
	return &Provider{cfg: cfg, run: defaultRun, cache: NewInMemorySessionCache()}
}

// WithCache swaps the SessionCache implementation. Returns the Provider
// for chaining.
func (p *Provider) WithCache(c SessionCache) *Provider {
	if c != nil {
		p.cache = c
	}
	return p
}

func (p *Provider) knowsSession(id string) bool { return p.cache.IsKnown(id) }
func (p *Provider) rememberSession(id string)   { p.cache.Remember(id) }

// Name returns the provider identifier.
func (*Provider) Name() string { return "claudecli" }

// Complete runs a one-shot claude invocation. The full message history
// is NOT sent — claude maintains conversation state itself.
//
// Session semantics:
//
//   - `claude -p --session-id <uuid>` creates a new session; if the uuid
//     already exists in claude's store the CLI errors "already in use".
//   - `claude -p --resume <uuid>` resumes an existing session; errors if
//     the uuid is unknown.
//
// We pick one based on an in-memory cache (`p.seen`) primed as calls
// succeed. On a cold-start cache miss where claude has state from a
// previous rousseau run we optimistically try --session-id first, catch
// the "already in use" error, and retry with --resume.
func (p *Provider) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	prompt, images, err := lastUserContent(req.Messages)
	if err != nil {
		return agent.Response{}, err
	}
	imagePaths, cleanup, err := writeImages(images)
	if err != nil {
		return agent.Response{}, err
	}
	defer cleanup()

	sessionFlag := "--session-id"
	if req.SessionID != "" && p.knowsSession(req.SessionID) {
		sessionFlag = "--resume"
	}

	resp, err := p.invoke(ctx, sessionFlag, req, prompt, imagePaths)
	if err != nil && sessionFlag == "--session-id" && strings.Contains(err.Error(), "already in use") {
		// Cold-start miss: claude has state from a previous rousseau run.
		p.rememberSession(req.SessionID)
		resp, err = p.invoke(ctx, "--resume", req, prompt, imagePaths)
	}
	if err != nil {
		return agent.Response{}, err
	}
	if req.SessionID != "" {
		p.rememberSession(req.SessionID)
	}
	return resp, nil
}

func (p *Provider) invoke(ctx context.Context, sessionFlag string, req agent.Request, prompt string, imagePaths []string) (agent.Response, error) {
	args := []string{"--print", "--output-format", "json"}
	if req.SessionID != "" {
		args = append(args, sessionFlag, req.SessionID)
	}
	// --system-prompt is honoured on session creation and ignored on
	// resume, which is exactly the semantics we want.
	if req.System != "" {
		args = append(args, "--system-prompt", req.System)
	}
	if p.cfg.Model != "" {
		args = append(args, "--model", p.cfg.Model)
	}
	if p.cfg.PermissionMode != "" {
		args = append(args, "--permission-mode", p.cfg.PermissionMode)
	}
	args = append(args, p.cfg.ExtraArgs...)
	// The claude CLI accepts one or more --image paths preceding the
	// prompt. Attach every temp-file image from the last user message.
	for _, path := range imagePaths {
		args = append(args, "--image", path)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, p.cfg.Binary, args...)
	out, err := p.run(cmd)
	if err != nil {
		return agent.Response{}, fmt.Errorf("claudecli: run: %w: %s", err, truncate(string(out), 400))
	}
	return parseResult(out)
}

// cliResult is the subset of `claude -p --output-format json`'s output
// that we care about.
type cliResult struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	APIErrorStatus json.RawMessage `json:"api_error_status"`
}

func parseResult(raw []byte) (agent.Response, error) {
	// `claude -p` sometimes emits a leading log line before the JSON.
	// Locate the first '{' and parse from there.
	i := bytes.IndexByte(raw, '{')
	if i < 0 {
		return agent.Response{}, fmt.Errorf("claudecli: no JSON in output: %s", truncate(string(raw), 200))
	}
	var res cliResult
	if err := json.Unmarshal(raw[i:], &res); err != nil {
		return agent.Response{}, fmt.Errorf("claudecli: parse JSON: %w", err)
	}
	if res.IsError {
		msg := res.Result
		if msg == "" {
			msg = string(res.APIErrorStatus)
		}
		return agent.Response{}, fmt.Errorf("claudecli: model error: %s", truncate(msg, 400))
	}
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: res.Result}},
		},
		StopReason: mapStop(res.StopReason),
		Usage: agent.Usage{
			InputTokens:  res.Usage.InputTokens,
			OutputTokens: res.Usage.OutputTokens,
		},
	}, nil
}

func mapStop(s string) agent.StopReason {
	switch s {
	case "end_turn":
		return agent.StopEndTurn
	case "max_tokens":
		return agent.StopMaxTokens
	default:
		return agent.StopEndTurn
	}
}

// lastUserContent walks the messages from the tail and returns the
// text + images from the most recent user turn that carries any
// non-empty content. Empty text is allowed when at least one image
// is present (image-only turns).
func lastUserContent(msgs []agent.Message) (text string, images []*agent.Image, err error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != agent.RoleUser {
			continue
		}
		var b strings.Builder
		var imgs []*agent.Image
		for _, c := range msgs[i].Content {
			switch c.Kind {
			case agent.ContentText:
				if c.Text != "" {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(c.Text)
				}
			case agent.ContentImage:
				if c.Image != nil {
					imgs = append(imgs, c.Image)
				}
			}
		}
		if b.Len() == 0 && len(imgs) == 0 {
			continue
		}
		return b.String(), imgs, nil
	}
	return "", nil, errors.New("claudecli: no user content to send")
}

// writeImages persists every image to a temp file and returns their
// paths + a cleanup function that removes the files after the CLI
// call. Empty images list is a noop.
func writeImages(images []*agent.Image) (paths []string, cleanup func(), err error) {
	if len(images) == 0 {
		return nil, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "rousseau-cli-imgs-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) } //nolint:errcheck // best-effort cleanup
	for i, img := range images {
		ext := extensionFor(img.MediaType)
		path := filepath.Join(dir, fmt.Sprintf("img-%d%s", i, ext))
		if err := os.WriteFile(path, img.Data, 0o600); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		paths = append(paths, path)
	}
	return paths, cleanup, nil
}

// extensionFor maps common MIME types to file extensions. Unknown
// types fall back to .bin.
func extensionFor(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".bin"
}

func defaultRun(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
