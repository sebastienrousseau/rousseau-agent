package vertex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// injected wraps an httptest.Server as an *http.Client so tests can
// exercise New + Complete without ADC.
func injectedClient(srv *httptest.Server) *http.Client {
	return &http.Client{Transport: &redirectTransport{target: srv.URL}}
}

// redirectTransport rewrites the request URL to point at the test
// server while preserving the path (so the provider still hits
// /rawPredict inside the test).
type redirectTransport struct{ target string }

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if u, err := parseURL(r.target); err == nil {
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
	}
	return http.DefaultTransport.RoundTrip(req)
}

func parseURL(raw string) (struct{ Scheme, Host string }, error) {
	u, err := (&http.Request{URL: nil}).URL, error(nil)
	_ = u
	_ = err
	// A minimal URL parser sufficient for httptest's http://host:port.
	if !strings.HasPrefix(raw, "http") {
		return struct{ Scheme, Host string }{}, io.ErrUnexpectedEOF
	}
	trim := strings.TrimPrefix(raw, "http://")
	scheme := "http"
	if strings.HasPrefix(raw, "https://") {
		trim = strings.TrimPrefix(raw, "https://")
		scheme = "https"
	}
	return struct{ Scheme, Host string }{scheme, trim}, nil
}

func TestNew_RequiresProject(t *testing.T) {
	_, err := New(context.Background(), Config{Region: "r", Model: "m", HTTPClient: &http.Client{}})
	assert.Error(t, err)
}

func TestNew_RequiresRegion(t *testing.T) {
	_, err := New(context.Background(), Config{Project: "p", Model: "m", HTTPClient: &http.Client{}})
	assert.Error(t, err)
}

func TestNew_RequiresModel(t *testing.T) {
	_, err := New(context.Background(), Config{Project: "p", Region: "r", HTTPClient: &http.Client{}})
	assert.Error(t, err)
}

func TestNew_DefaultsMaxTokens(t *testing.T) {
	p, err := New(context.Background(), Config{
		Project: "p", Region: "r", Model: "m",
		HTTPClient: &http.Client{},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(4096), p.cfg.MaxTokens)
	assert.Equal(t, "vertex", p.Name())
}

func TestComplete_HappyPath(t *testing.T) {
	var seen []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body) //nolint:errcheck // test fixture
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_01","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"hi from vertex"}],"usage":{"input_tokens":8,"output_tokens":4}}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	p, err := New(context.Background(), Config{
		Project: "p", Region: "us-central1", Model: "m",
		HTTPClient: injectedClient(srv),
	})
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), agent.Request{
		System:   "you help",
		Messages: []agent.Message{agent.NewUserText("hello")},
	})
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, "hi from vertex", resp.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, resp.StopReason)
	assert.Equal(t, 8, resp.Usage.InputTokens)
	assert.Equal(t, 4, resp.Usage.OutputTokens)

	var sent vertexRequest
	require.NoError(t, json.NewDecoder(strings.NewReader(string(seen))).Decode(&sent))
	assert.Equal(t, "vertex-2023-10-16", sent.AnthropicVersion)
	assert.Equal(t, "you help", sent.System)
	assert.Len(t, sent.Messages, 1)
}

func TestComplete_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"denied"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	p, err := New(context.Background(), Config{
		Project: "p", Region: "r", Model: "m",
		HTTPClient: injectedClient(srv),
	})
	require.NoError(t, err)
	_, err = p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestBuildVertexBody_ToolBlocks(t *testing.T) {
	req := agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleAssistant, Content: []agent.Content{
				{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{
					ID: "tu-1", Name: "grep", Input: json.RawMessage(`{"pattern":"x"}`),
				}},
			},
		}, {
			Role: agent.RoleUser, Content: []agent.Content{
				{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{
					ToolUseID: "tu-1", Output: "matched", IsError: false,
				}},
			},
		}},
	}
	body, err := buildVertexBody(req, 100)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"tool_use_id":"tu-1"`)
	assert.Contains(t, string(body), `"tool_use"`)
	assert.Contains(t, string(body), `"grep"`)
}

func TestBuildVertexBody_MalformedToolUseErrors(t *testing.T) {
	req := agent.Request{Messages: []agent.Message{{
		Role: agent.RoleAssistant, Content: []agent.Content{
			{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{
				ID: "1", Name: "n", Input: json.RawMessage(`not json`),
			}},
		},
	}}}
	_, err := buildVertexBody(req, 100)
	assert.Error(t, err)
}

func TestParseVertexResponse_ToolUseInResponse(t *testing.T) {
	raw := []byte(`{
    "stop_reason":"tool_use",
    "content":[{"type":"tool_use","id":"call_1","name":"read","input":{"path":"/tmp/x"}}]
  }`)
	resp, err := parseVertexResponse(raw)
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, agent.ContentToolUse, resp.Message.Content[0].Kind)
	assert.Equal(t, agent.StopToolUse, resp.StopReason)
}

func TestParseVertexResponse_MalformedJSON(t *testing.T) {
	_, err := parseVertexResponse([]byte(`not json`))
	assert.Error(t, err)
}

func TestMapStop(t *testing.T) {
	assert.Equal(t, agent.StopEndTurn, mapStop("end_turn"))
	assert.Equal(t, agent.StopToolUse, mapStop("tool_use"))
	assert.Equal(t, agent.StopMaxTokens, mapStop("max_tokens"))
	assert.Equal(t, agent.StopOther, mapStop("weird"))
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hi", truncate("hi", 10))
	assert.True(t, strings.HasSuffix(truncate("hello world", 5), "…"))
}

// TestNew_CredentialsFileMissingErrors exercises the read-file branch
// when CredentialsFile is set but points at a non-existent path.
func TestNew_CredentialsFileMissingErrors(t *testing.T) {
	_, err := New(context.Background(), Config{
		Project: "p", Region: "r", Model: "m",
		CredentialsFile: "/nonexistent/no-such-file.json",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read credentials")
}

// TestNew_CredentialsFileMalformedErrors exercises the JSON parse
// branch when the file exists but does not contain valid ADC JSON.
func TestNew_CredentialsFileMalformedErrors(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "creds-*.json")
	require.NoError(t, err)
	_, _ = tmp.WriteString("not-valid-json") //nolint:errcheck // test fixture
	_ = tmp.Close()                          //nolint:errcheck // test fixture

	_, err = New(context.Background(), Config{
		Project: "p", Region: "r", Model: "m",
		CredentialsFile: tmp.Name(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credentials")
}

// TestReadAllFile_ReadsBytes calls the tiny indirection directly.
func TestReadAllFile_ReadsBytes(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "rf-*.txt")
	require.NoError(t, err)
	_, _ = tmp.WriteString("hello vertex") //nolint:errcheck // test fixture
	_ = tmp.Close()                        //nolint:errcheck // test fixture

	b, err := readAllFile(tmp.Name())
	require.NoError(t, err)
	assert.Equal(t, "hello vertex", string(b))

	_, err = readAllFile("/no/such/path")
	assert.Error(t, err)
}
