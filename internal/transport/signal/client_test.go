package signal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestNew_RequiresAccount(t *testing.T) {
	_, err := New(Config{}, silentLogger())
	assert.Error(t, err)
}

func TestNew_DefaultsBinary(t *testing.T) {
	c, err := New(Config{Account: "+15551234567"}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "signal-cli", c.cfg.Binary)
	assert.Equal(t, "signal", c.Name())
}

func TestNew_KeepsExplicitBinary(t *testing.T) {
	c, err := New(Config{Account: "+1", Binary: "/opt/signal-cli"}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "/opt/signal-cli", c.cfg.Binary)
}

func TestDeliver_NotConnected(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "+2", "hi")
	assert.Error(t, err)
}

func TestStopIdempotent(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}

// stubWriter captures encoded jsonRPC requests for assertion.
type stubWriter struct {
	buf bytes.Buffer
	err error
}

func (s *stubWriter) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.buf.Write(p)
}
func (s *stubWriter) Close() error { return nil }

func TestJSONWriter_EncodesRequest(t *testing.T) {
	sw := &stubWriter{}
	jw := &jsonWriter{w: sw, enc: json.NewEncoder(sw)}
	require.NoError(t, jw.write(jsonRPCRequest{
		JSONRPC: "2.0", ID: 1, Method: "send",
		Params: map[string]any{"recipient": []string{"+1"}, "message": "hi"},
	}))
	got := sw.buf.String()
	assert.Contains(t, got, `"method":"send"`)
	assert.Contains(t, got, `"message":"hi"`)
}

func TestHandleFrame_IgnoresAcks(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	err = c.handleFrame(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		t.Fatal("handler should not be invoked for ack")
		return "", nil
	}))
	assert.NoError(t, err)
}

func TestHandleFrame_RoutesReceive(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)

	var seen transport.IncomingMessage
	handled := false
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		handled = true
		return "", nil
	})

	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+2","sourceNumber":"+2","timestamp":` +
		"1700000000000" +
		`,"dataMessage":{"message":"hello signal"}},"account":"+1"}}`)
	err = c.handleFrame(context.Background(), frame, handler)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, "+2", seen.From)
	assert.Equal(t, "hello signal", seen.Body)
	assert.Equal(t, time.UnixMilli(1700000000000), seen.At)
}

func TestHandleFrame_EmptyMessageIsIgnored(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		t.Fatal("handler should not be invoked for empty body")
		return "", nil
	})
	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+2","dataMessage":{"message":""}}}}`)
	require.NoError(t, c.handleFrame(context.Background(), frame, handler))
}

func TestHandleFrame_MalformedIsError(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil })
	err = c.handleFrame(context.Background(), []byte(`{not json`), handler)
	assert.Error(t, err)
}

func TestPrefixWriter_LogsPerLine(t *testing.T) {
	pw := &prefixWriter{logger: silentLogger()}
	n, err := pw.Write([]byte("first\nsecond\npartial"))
	require.NoError(t, err)
	assert.Equal(t, len("first\nsecond\npartial"), n)
	// A follow-up write completes the last line.
	_, err = pw.Write([]byte("-line\n"))
	require.NoError(t, err)
}

func TestIndexByte(t *testing.T) {
	assert.Equal(t, 2, indexByte([]byte("abcd"), 'c'))
	assert.Equal(t, -1, indexByte([]byte("abcd"), 'z'))
}

// TestHandleFrame_FallsBackToSource covers the branch where sourceNumber
// is empty and the code falls back to the raw source (Signal UUID form).
func TestHandleFrame_FallsBackToSource(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return "", nil
	})
	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"uuid-abc","dataMessage":{"message":"y"}}}}`)
	require.NoError(t, c.handleFrame(context.Background(), frame, handler))
	assert.Equal(t, "uuid-abc", seen.From)
}

// TestHandleFrame_HandlerErrorSuppressed exercises the branch that logs
// the handler error but returns nil.
func TestHandleFrame_HandlerErrorSuppressed(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", errors.New("boom")
	})
	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+2","dataMessage":{"message":"y"}}}}`)
	assert.NoError(t, c.handleFrame(context.Background(), frame, handler))
}

// TestHandleFrame_MalformedReceiveParams covers the branch where the
// envelope shape is valid but params fail to decode as receiveParams.
func TestHandleFrame_MalformedReceiveParams(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil })
	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":"not-an-object"}`)
	assert.Error(t, c.handleFrame(context.Background(), frame, handler))
}

// TestHandleFrame_RepliesViaJSONWriter exercises the reply path where
// the handler returns a non-empty string and the client encodes a
// send request onto the stubbed stdin.
func TestHandleFrame_RepliesViaJSONWriter(t *testing.T) {
	c, err := New(Config{Account: "+1", ReplyHeader: "[bot] "}, silentLogger())
	require.NoError(t, err)
	sw := &stubWriter{}
	c.stdin = &jsonWriter{w: sw, enc: json.NewEncoder(sw)}

	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "pong", nil
	})
	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+2","sourceNumber":"+2","dataMessage":{"message":"ping"}}}}`)
	require.NoError(t, c.handleFrame(context.Background(), frame, handler))
	got := sw.buf.String()
	assert.Contains(t, got, `"method":"send"`)
	assert.Contains(t, got, `"[bot] pong"`)
	assert.Contains(t, got, `"+2"`)
}

// TestDeliver_ConnectedEncodesRequest checks that Deliver writes a
// well-formed jsonRPC send request onto the stubbed stdin.
func TestDeliver_ConnectedEncodesRequest(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	sw := &stubWriter{}
	c.stdin = &jsonWriter{w: sw, enc: json.NewEncoder(sw)}
	require.NoError(t, c.Deliver(context.Background(), "+2", "hi"))
	got := sw.buf.String()
	assert.Contains(t, got, `"method":"send"`)
	assert.Contains(t, got, `"hi"`)
}

// TestPump_DispatchesFrames drives pump with a multi-frame buffer and
// verifies handleFrame is invoked for the receive frame while acks are
// silently skipped. Exercises the primary loop and its EOF exit path.
func TestPump_DispatchesFrames(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)

	body := "" +
		`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n" + // ack — ignored
		`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+2","sourceNumber":"+2","dataMessage":{"message":"hi"}}}}` + "\n" +
		`{not-json}` + "\n" // malformed — logged, loop continues

	var seen []string
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = append(seen, m.Body)
		return "", nil
	})
	require.NoError(t, c.pump(context.Background(), bytes.NewBufferString(body), handler))
	require.Len(t, seen, 1)
	assert.Equal(t, "hi", seen[0])
}

// TestPump_ExitsOnContextCancel verifies the ctx-cancel branch inside
// the scanner loop.
func TestPump_ExitsOnContextCancel(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel; first Scan returns bytes, ctx check triggers exit
	body := `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"
	err = c.pump(ctx, bytes.NewBufferString(body), transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	assert.ErrorIs(t, err, context.Canceled)
}

// TestJSONWriter_Close closes the underlying writer.
func TestJSONWriter_Close(t *testing.T) {
	sw := &stubWriter{}
	jw := &jsonWriter{w: sw, enc: json.NewEncoder(sw)}
	assert.NoError(t, jw.Close())
}

// TestStart_RequiresHandler covers the nil-handler guard.
func TestStart_RequiresHandler(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	assert.Error(t, c.Start(context.Background(), nil))
}

// TestStart_AlreadyStartedIsRejected exercises the double-start guard by
// pre-setting the internal cmd field. No process is actually spawned.
func TestStart_AlreadyStartedIsRejected(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	// Simulate an in-flight process — we only care that Start refuses.
	c.cmd = &exec.Cmd{}
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil })
	assert.ErrorContains(t, c.Start(context.Background(), handler), "already started")
}
