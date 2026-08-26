package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// These tests cover the transport and handler failure paths a
// well-behaved host never triggers: a broken stdout, a cancelled
// context mid-stream, malformed params, and backends that error.

// brokenWriter models a stdout pipe whose peer has gone away.
type brokenWriter struct{ err error }

func (b brokenWriter) Write([]byte) (int, error) { return 0, b.err }

// errBackend is a SessionsBackend whose reads fail, standing in for a
// database that has gone away underneath the MCP server.
type errBackend struct {
	listErr error
	cronErr error
	jobs    []sqlitestore.CronJob
}

func (e *errBackend) Search(context.Context, string, sqlitestore.SearchOptions) ([]sqlitestore.SearchHit, error) {
	return nil, nil
}
func (e *errBackend) List(context.Context, int) ([]state.Summary, error) { return nil, e.listErr }
func (e *errBackend) Load(context.Context, string) (*agent.Session, error) {
	return nil, errors.New("unused")
}
func (e *errBackend) CronList(context.Context) ([]sqlitestore.CronJob, error) {
	return e.jobs, e.cronErr
}

// -- server transport --------------------------------------------------

func TestNewServer_NilLoggerGetsDefault(t *testing.T) {
	s := NewServer("rousseau-test", "0.0.0", nil)
	require.NotNil(t, s.logger)
	require.NoError(t, s.Register(ToolSpec{Name: "ok", Handler: dummyHandler()}))

	resp := call(t, s, MethodToolsList, json.RawMessage(`1`), nil)
	require.Nil(t, resp.Error)
}

func TestServe_StopsWhenContextIsCancelled(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := Envelope{JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: MethodPing}
	b, err := json.Marshal(req)
	require.NoError(t, err)
	out := &bytes.Buffer{}

	err = s.Serve(ctx, bytes.NewReader(append(b, '\n')), out)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, out.String(), "no response may be written after cancellation")
}

func TestServe_WriteFailureAbortsTheLoop(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	req := Envelope{JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: MethodPing}
	b, err := json.Marshal(req)
	require.NoError(t, err)

	err = s.Serve(context.Background(), bytes.NewReader(append(b, '\n')),
		brokenWriter{err: errors.New("stdout closed")})
	require.Error(t, err)
	assert.ErrorContains(t, err, "write response")
	assert.ErrorContains(t, err, "stdout closed")
}

// TestServe_ParseErrorReplyFailureIsLoggedNotFatal proves a broken
// stdout during the parse-error reply does not take the loop down —
// the server keeps draining stdin.
func TestServe_ParseErrorReplyFailureIsLoggedNotFatal(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	in := strings.NewReader("{not json}\n{also not json}\n")
	err := s.Serve(context.Background(), in, brokenWriter{err: errors.New("stdout closed")})
	assert.NoError(t, err, "parse-error replies are best-effort")
}

func TestServe_SkipsBlankLines(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	req := Envelope{JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: MethodPing}
	b, err := json.Marshal(req)
	require.NoError(t, err)

	out := &bytes.Buffer{}
	in := strings.NewReader("\n\n" + string(b) + "\n")
	require.NoError(t, s.Serve(context.Background(), in, out))

	var resp Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &resp))
	assert.Nil(t, resp.Error)
}

func TestDispatch_RejectsWrongJSONRPCVersion(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	resp := s.dispatch(context.Background(), Envelope{JSONRPC: "1.0", ID: json.RawMessage(`1`), Method: MethodPing})
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInvalidRequest, resp.Error.Code)
}

// -- the read-only method surface --------------------------------------

func TestServer_EmptyResourceAndPromptSurfaces(t *testing.T) {
	s := NewServer("t", "0", silentLogger())

	res := call(t, s, MethodResourcesList, json.RawMessage(`1`), nil)
	require.Nil(t, res.Error)
	assert.JSONEq(t, `{"resources":[]}`, string(res.Result))

	prompts := call(t, s, MethodPromptsList, json.RawMessage(`2`), nil)
	require.Nil(t, prompts.Error)
	assert.JSONEq(t, `{"prompts":[]}`, string(prompts.Result))
}

func TestServer_ShutdownAcknowledged(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	resp := call(t, s, MethodShutdown, json.RawMessage(`9`), nil)
	require.Nil(t, resp.Error)
	assert.JSONEq(t, `{}`, string(resp.Result))
}

// -- tools/call ---------------------------------------------------------

func TestToolsCall_MalformedParams(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	resp := call(t, s, MethodToolsCall, json.RawMessage(`1`), json.RawMessage(`"not-an-object"`))
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInvalidParams, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "cannot decode params")
}

// TestToolsCall_NilContentBecomesEmptyArray keeps the wire contract:
// hosts expect `content` to be an array, never null.
func TestToolsCall_NilContentBecomesEmptyArray(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	s.MustRegister(ToolSpec{Name: "quiet", Handler: dummyHandler()})
	params, err := json.Marshal(ToolsCallParams{Name: "quiet"})
	require.NoError(t, err)

	resp := call(t, s, MethodToolsCall, json.RawMessage(`1`), json.RawMessage(params))
	require.Nil(t, resp.Error)
	assert.Contains(t, string(resp.Result), `"content":[]`)

	var r ToolsCallResult
	require.NoError(t, json.Unmarshal(resp.Result, &r))
	assert.NotNil(t, r.Content)
	assert.Empty(t, r.Content)
}

func TestOKResponse_UnmarshalableResultDegradesToInternalError(t *testing.T) {
	resp := okResponse(json.RawMessage(`7`), make(chan int))
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInternalError, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "marshal result")
	assert.Equal(t, "7", string(resp.ID))
}

func TestTextContent_WrapsPlainText(t *testing.T) {
	got := TextContent("hi")
	require.Len(t, got, 1)
	assert.Equal(t, "text", got[0].Type)
	assert.Equal(t, "hi", got[0].Text)
}

// -- rousseau tool handlers --------------------------------------------

func TestSearchSessionsTool_MalformedArgs(t *testing.T) {
	spec := searchSessionsTool(&errBackend{})
	_, err := callTool(t, spec, `{"query":`)
	require.Error(t, err)
	assert.ErrorContains(t, err, "parse args")
}

func TestListSessionsTool_BackendError(t *testing.T) {
	spec := listSessionsTool(&errBackend{listErr: errors.New("db offline")})
	_, err := callTool(t, spec, `{}`)
	assert.ErrorContains(t, err, "db offline")
}

func TestReadSessionTool_MalformedArgs(t *testing.T) {
	spec := readSessionTool(&errBackend{})
	_, err := callTool(t, spec, `{"id":`)
	assert.Error(t, err)
}

func TestCronListTool_BackendError(t *testing.T) {
	spec := cronListTool(&errBackend{cronErr: errors.New("db offline")})
	_, err := callTool(t, spec, `{}`)
	assert.ErrorContains(t, err, "db offline")
}

func TestCronListTool_DisabledJobsRenderAsOff(t *testing.T) {
	spec := cronListTool(&errBackend{jobs: []sqlitestore.CronJob{
		{Name: "nightly", CronExpr: "0 2 * * *", Prompt: "digest", DeliverTo: "u@x", Enabled: false},
	}})
	content, err := callTool(t, spec, `{}`)
	require.NoError(t, err)
	require.Len(t, content, 1)
	assert.Contains(t, content[0].Text, "nightly [off]")
}

func TestMustSchema_PanicsOnUnmarshalableSchema(t *testing.T) {
	assert.Panics(t, func() { mustSchema(map[string]any{"bad": make(chan int)}) })
}
