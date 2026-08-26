package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp"
)

// These tests reach the internal request/write/notify/readLoop paths
// that a well-behaved subprocess never triggers: an unwritable stdin,
// a stdout that errors mid-stream, and payloads that cannot be
// marshalled. They stay in-package because those failure modes are not
// reachable through the exported surface.

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// failWriteCloser fails every Write, standing in for a subprocess that
// died between the handshake and the next request (EPIPE).
type failWriteCloser struct{ err error }

func (f failWriteCloser) Write([]byte) (int, error) { return 0, f.err }
func (f failWriteCloser) Close() error              { return nil }

type nopWriteCloser struct{ buf bytes.Buffer }

func (n *nopWriteCloser) Write(p []byte) (int, error) { return n.buf.Write(p) }
func (n *nopWriteCloser) Close() error                { return nil }

// errReader yields one line then a hard read error, mimicking a pipe
// torn down mid-stream.
type errReader struct {
	data string
	done bool
	err  error
}

func (e *errReader) Read(p []byte) (int, error) {
	if !e.done {
		e.done = true
		n := copy(p, e.data)
		return n, nil
	}
	return 0, e.err
}

// newTestClient builds a Client that is wired up enough to exercise
// request/write/notify without spawning a subprocess.
func newTestClient(stdin io.WriteCloser) *Client {
	return &Client{
		name:      "unit",
		stdin:     stdin,
		logger:    testLogger(),
		timeout:   time.Second,
		done:      make(chan struct{}),
		stderrBuf: newBoundedBuffer(1024),
	}
}

func TestRequest_WriteFailureIsWrapped(t *testing.T) {
	c := newTestClient(failWriteCloser{err: errors.New("broken pipe")})
	err := c.request(context.Background(), mcp.MethodToolsList, nil, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "write tools/list")
	assert.ErrorContains(t, err, "broken pipe")
}

func TestRequest_UnmarshalableParamsFailBeforeWrite(t *testing.T) {
	stdin := &nopWriteCloser{}
	c := newTestClient(stdin)
	err := c.request(context.Background(), "custom/method", make(chan int), nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "marshal params for custom/method")
	assert.Zero(t, stdin.buf.Len(), "nothing may hit the wire when params are unmarshalable")
}

func TestRequest_AbortsWhenClientClosesMidFlight(t *testing.T) {
	c := newTestClient(&nopWriteCloser{})
	c.timeout = 10 * time.Second
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(c.done) // simulate Close() landing while a request is parked
	}()
	err := c.request(context.Background(), mcp.MethodToolsList, nil, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "closed while awaiting response")
}

func TestWrite_UnmarshalableEnvelope(t *testing.T) {
	c := newTestClient(&nopWriteCloser{})
	// RPCError.Data is an `any`; a channel makes the envelope itself
	// unmarshalable, which is the only way write's marshal step fails.
	err := c.write(mcp.Envelope{JSONRPC: "2.0", Error: &mcp.RPCError{Data: make(chan int)}})
	require.Error(t, err)
	assert.ErrorContains(t, err, "marshal")
}

func TestNotify_SerialisesParams(t *testing.T) {
	stdin := &nopWriteCloser{}
	c := newTestClient(stdin)
	require.NoError(t, c.notify("notifications/custom", map[string]string{"k": "v"}))

	var env mcp.Envelope
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(stdin.buf.Bytes()), &env))
	assert.Equal(t, "notifications/custom", env.Method)
	assert.Empty(t, env.ID, "notifications must not carry an id")
	assert.JSONEq(t, `{"k":"v"}`, string(env.Params))
}

func TestNotify_UnmarshalableParams(t *testing.T) {
	c := newTestClient(&nopWriteCloser{})
	err := c.notify("notifications/custom", make(chan int))
	require.Error(t, err)
	assert.ErrorContains(t, err, "marshal notification notifications/custom")
}

func TestReadLoop_SurvivesStdoutReadError(t *testing.T) {
	c := newTestClient(&nopWriteCloser{})
	respCh := make(chan mcp.Envelope, 1)
	c.pending.Store(int64(7), respCh)

	r := &errReader{
		data: `{"jsonrpc":"2.0","id":7,"result":{"content":[]}}` + "\n",
		err:  errors.New("pipe torn down"),
	}
	c.readLoop(r) // returns once the reader errors

	select {
	case env := <-respCh:
		assert.Equal(t, "7", string(env.ID))
	default:
		t.Fatal("the line delivered before the error should still be routed")
	}
}

func TestReadLoop_DropsLateResponseWithoutBlocking(t *testing.T) {
	c := newTestClient(&nopWriteCloser{})
	// A full channel models a requester that already timed out: the
	// read loop must not block on the late arrival.
	respCh := make(chan mcp.Envelope, 1)
	respCh <- mcp.Envelope{ID: json.RawMessage(`1`)}
	c.pending.Store(int64(1), respCh)

	done := make(chan struct{})
	go func() {
		c.readLoop(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop blocked on a late response")
	}
}

func TestDrainStderr_LogsReadFailure(t *testing.T) {
	buf := newBoundedBuffer(64)
	drainStderr(&errReader{data: "partial output", err: errors.New("read failed")}, buf, testLogger())
	assert.Equal(t, "partial output", buf.Tail(), "bytes read before the failure are retained")
}

func TestDrainStderr_CapturesFullStream(t *testing.T) {
	buf := newBoundedBuffer(64)
	drainStderr(strings.NewReader("server said hello"), buf, testLogger())
	assert.Equal(t, "server said hello", buf.Tail())
}

func TestClosedNow_ReflectsCloseFlag(t *testing.T) {
	c := newTestClient(&nopWriteCloser{})
	assert.False(t, c.closedNow())
	c.closed = true
	assert.True(t, c.closedNow())
}
