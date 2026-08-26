package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// noopHandler is a handler that never replies.
func noopHandler() transport.Handler {
	return transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", nil
	})
}

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	c, err := New(Config{Token: "tok"}, nil)
	require.NoError(t, err)
	assert.NotNil(t, c.logger)
	assert.Equal(t, "discord", c.Name())
}

// TestStart_ReturnsImmediatelyWhenStopped proves Stop() before Start()
// short-circuits the session loop without dialling.
func TestStart_ReturnsImmediatelyWhenStopped(t *testing.T) {
	var dials atomic.Int32
	c, err := New(Config{
		Token: "bot",
		DialWebSocket: func(context.Context, string) (WSConn, error) {
			dials.Add(1)
			return nil, errors.New("should not dial")
		},
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())

	assert.NoError(t, c.Start(context.Background(), noopHandler()))
	assert.Zero(t, dials.Load())
}

// TestStart_BackoffElapsesAndRedials exercises the 2s backoff timer arm
// of the retry select (as opposed to the ctx.Done arm).
func TestStart_BackoffElapsesAndRedials(t *testing.T) {
	var dials atomic.Int32
	c, err := New(Config{
		Token: "bot",
		DialWebSocket: func(context.Context, string) (WSConn, error) {
			dials.Add(1)
			return nil, errors.New("boom")
		},
	}, silentLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	err = c.Start(ctx, noopHandler())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, dials.Load(), int32(2), "backoff should elapse and redial")
}

// TestStop_ClosesActiveConnection covers the live-connection branch of
// Stop. The connection is installed directly (same package) to avoid a
// race with a background session goroutine.
func TestStop_ClosesActiveConnection(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{}
	c.mu.Lock()
	c.conn = ws
	c.mu.Unlock()

	require.NoError(t, c.Stop())
	assert.True(t, ws.closed)
	c.mu.Lock()
	assert.Nil(t, c.conn)
	c.mu.Unlock()
}

// TestPump_DispatchesFramesAndLogsFrameErrors drives the pump loop body
// with a good frame and a malformed frame before the read error ends it.
func TestPump_DispatchesFramesAndLogsFrameErrors(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{inbox: [][]byte{
		[]byte(`{"op":10}`),       // HELLO → identify write
		[]byte(`{"op":11}`),       // heartbeat ack
		[]byte(`not json at all`), // handleFrame error → logged, loop continues
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err = c.pump(ctx, ws, noopHandler())
	assert.Error(t, err, "pump ends when the socket stops yielding frames")

	ws.mu.Lock()
	defer ws.mu.Unlock()
	require.Len(t, ws.writes, 1, "HELLO should have produced exactly one Identify")
	var identify struct {
		Op int `json:"op"`
		D  struct {
			Token   string `json:"token"`
			Intents int    `json:"intents"`
		} `json:"d"`
	}
	require.NoError(t, json.Unmarshal(ws.writes[0], &identify))
	assert.Equal(t, 2, identify.Op)
	assert.Equal(t, "t", identify.D.Token)
	assert.Equal(t, intentGuildMessages|intentDirectMessages|intentMessageContent, identify.D.Intents)
}

func TestHandleFrame_UnknownOpcodeIgnored(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	// 1 = heartbeat request, 7 = reconnect, 9 = invalid session.
	for _, op := range []int{1, 7, 9, 42} {
		raw := []byte(`{"op":` + strconv.Itoa(op) + `,"s":3}`)
		assert.NoError(t, c.handleFrame(context.Background(), &fakeWS{}, raw, noopHandler()))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Equal(t, int64(3), c.seq, "sequence number is tracked for every opcode")
}

func TestDispatch_MalformedPayloads(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		errIs string
	}{
		{"ready not an object", `{"op":0,"t":"READY","d":[1,2,3]}`, "parse ready"},
		{"message not an object", `{"op":0,"t":"MESSAGE_CREATE","d":"nope"}`, "parse message"},
		{"ready wrong field type", `{"op":0,"t":"READY","d":{"user":42}}`, "parse ready"},
	}
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := c.handleFrame(context.Background(), &fakeWS{}, []byte(tc.frame), noopHandler())
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.errIs)
		})
	}
}

// TestDispatch_SelfAuthoredMessageSkipped covers the belt-and-braces
// self-ID guard for a message that is not flagged bot=true.
func TestDispatch_SelfAuthoredMessageSkipped(t *testing.T) {
	var handled atomic.Int32
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	c.mu.Lock()
	c.selfID = "self-1"
	c.mu.Unlock()

	frame := []byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"channel_id":"c","content":"hello","author":{"id":"self-1"}}}`)
	err = c.handleFrame(context.Background(), &fakeWS{},
		frame,
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			handled.Add(1)
			return "", nil
		}))
	require.NoError(t, err)
	assert.Zero(t, handled.Load(), "self-authored message must not reach the handler")
}

func TestPostMessage_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr string
	}{
		{"unbuildable request", "http://exa\x7fmple.invalid", "build request"},
		{"transport failure", "http://127.0.0.1:1", "post"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(Config{
				Token:      "t",
				BaseURL:    tc.baseURL,
				HTTPClient: &http.Client{Timeout: 2 * time.Second},
			}, silentLogger())
			require.NoError(t, err)
			err = c.Deliver(context.Background(), "chan", "body")
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestDefaultDial_HandshakeRejectedClosesBody points the real dialer at a
// plain HTTP server that refuses the upgrade, so websocket.Dial returns
// both a response (whose body must be closed) and an error.
func TestDefaultDial_HandshakeRejectedClosesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("no upgrade for you")) //nolint:errcheck // fixture
	}))
	defer srv.Close()

	conn, err := defaultDial(context.Background(), "ws"+srv.URL[len("http"):])
	require.Error(t, err)
	assert.Nil(t, conn)
}

func TestDownloadAttachment_ErrorPaths(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"unbuildable request", "http://exa\x7fmple.invalid/a.ogg"},
		{"transport failure", "http://127.0.0.1:1/a.ogg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(Config{
				Token:      "t",
				HTTPClient: &http.Client{Timeout: 2 * time.Second},
			}, silentLogger())
			require.NoError(t, err)
			_, err = c.downloadAttachment(context.Background(), tc.url)
			assert.Error(t, err)
		})
	}
}

// TestTranscribeAudio_DownloadErrorDropsMessage checks the transport
// drops (rather than stubs) a voice note it cannot fetch.
func TestTranscribeAudio_DownloadErrorDropsMessage(t *testing.T) {
	tr := &fakeTranscriber{reply: "should never be used"}
	c, err := New(Config{
		Token:       "t",
		Transcriber: tr,
		HTTPClient:  &http.Client{Timeout: 2 * time.Second},
	}, silentLogger())
	require.NoError(t, err)

	msg := &discordMessage{Attachments: []discordAttachment{
		{ContentType: "audio/ogg", URL: "http://127.0.0.1:1/voice.ogg"},
	}}
	assert.Empty(t, c.transcribeAudio(context.Background(), msg))
}
