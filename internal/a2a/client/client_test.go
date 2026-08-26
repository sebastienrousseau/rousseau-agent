package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// ---- helpers ---------------------------------------------------------

// newClient builds a Client pointed at base with sane test defaults.
func newClient(t *testing.T, base string, mutate ...func(*Config)) *Client {
	t.Helper()
	cfg := Config{Name: "peer", Endpoint: base, Timeout: 2 * time.Second}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := New(cfg)
	require.NoError(t, err)
	return c
}

// sse writes one SSE frame and flushes it.
func sse(t *testing.T, w http.ResponseWriter, upd a2a.TaskUpdate) {
	t.Helper()
	blob, err := json.Marshal(upd)
	require.NoError(t, err)
	_, err = fmt.Fprintf(w, "data: %s\n\n", blob)
	require.NoError(t, err)
	w.(http.Flusher).Flush()
}

// sseHeaders sets the standard event-stream headers.
func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

// writeAck answers POST /tasks with an accepted ack.
func writeAck(w http.ResponseWriter, taskID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"task_id": taskID, "status": "running"}) //nolint:errcheck // test writer
}

// roundTripFunc adapts a closure to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// drain collects every update on ch until it closes.
func drain(t *testing.T, ch <-chan a2a.TaskUpdate) []a2a.TaskUpdate {
	t.Helper()
	var out []a2a.TaskUpdate
	timeout := time.After(5 * time.Second)
	for {
		select {
		case upd, alive := <-ch:
			if !alive {
				return out
			}
			out = append(out, upd)
		case <-timeout:
			t.Fatal("timed out draining update channel")
			return out
		}
	}
}

// ---- New -------------------------------------------------------------

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "missing endpoint", cfg: Config{Name: "peer"}, wantErr: "Endpoint is required"},
		{name: "missing name", cfg: Config{Endpoint: "http://x"}, wantErr: "Name is required"},
		{name: "unparseable endpoint", cfg: Config{Name: "peer", Endpoint: "http://%zz"}, wantErr: "invalid Endpoint"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.cfg)
			require.Error(t, err)
			assert.Nil(t, c)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	t.Run("defaults are applied", func(t *testing.T) {
		c, err := New(Config{Name: "peer", Endpoint: "http://example.test"})
		require.NoError(t, err)
		assert.Equal(t, 60*time.Second, c.cfg.Timeout)
		assert.NotNil(t, c.client)
		assert.Equal(t, "example.test", c.base.Host)
		assert.Equal(t, "peer", c.Name())
	})

	t.Run("explicit timeout and http client are kept", func(t *testing.T) {
		hc := &http.Client{Timeout: time.Hour}
		c, err := New(Config{Name: "peer", Endpoint: "http://example.test", Timeout: time.Second, HTTPClient: hc})
		require.NoError(t, err)
		assert.Equal(t, time.Second, c.cfg.Timeout)
		assert.Same(t, hc, c.client)
	})

	t.Run("negative timeout falls back to the default", func(t *testing.T) {
		c, err := New(Config{Name: "peer", Endpoint: "http://example.test", Timeout: -5 * time.Second})
		require.NoError(t, err)
		assert.Equal(t, 60*time.Second, c.cfg.Timeout)
	})
}

// ---- FetchCard -------------------------------------------------------

func TestFetchCard(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		var gotPath, gotAuth, gotMethod string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
			_ = json.NewEncoder(w).Encode(a2a.CapabilityCard{ //nolint:errcheck // test writer
				AgentID: "peer-1",
				Name:    "peer",
				Version: "v9",
				Skills:  []a2a.SkillDescriptor{{Name: "review"}},
			})
		}))
		defer ts.Close()

		c := newClient(t, ts.URL, func(cfg *Config) { cfg.AuthHeader = "Bearer sekret" })
		card, err := c.FetchCard(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "peer-1", card.AgentID)
		require.Len(t, card.Skills, 1)
		assert.Equal(t, "review", card.Skills[0].Name)
		assert.Equal(t, "/.well-known/agent-capabilities", gotPath)
		assert.Equal(t, http.MethodGet, gotMethod)
		assert.Equal(t, "Bearer sekret", gotAuth)
	})

	t.Run("no auth header when unconfigured", func(t *testing.T) {
		var seen bool
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, seen = r.Header["Authorization"]
			_ = json.NewEncoder(w).Encode(a2a.CapabilityCard{}) //nolint:errcheck // test writer
		}))
		defer ts.Close()

		_, err := newClient(t, ts.URL).FetchCard(context.Background())
		require.NoError(t, err)
		assert.False(t, seen)
	})

	t.Run("non-200 surfaces status and body", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"invalid bearer token"}`) //nolint:errcheck // test writer
		}))
		defer ts.Close()

		_, err := newClient(t, ts.URL).FetchCard(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 403")
		assert.Contains(t, err.Error(), "invalid bearer token")
	})

	t.Run("malformed card body", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "not json") //nolint:errcheck // test writer
		}))
		defer ts.Close()

		_, err := newClient(t, ts.URL).FetchCard(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode card")
	})

	t.Run("transport error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := ts.URL
		ts.Close() // nothing is listening any more

		_, err := newClient(t, url).FetchCard(context.Background())
		require.Error(t, err)
	})

	t.Run("unbuildable request URL", func(t *testing.T) {
		// A base URL whose host carries a control byte is percent-escaped
		// by URL.String() into something url.Parse then rejects, so the
		// request never gets built. No traffic leaves the process.
		c := newClient(t, "http://example.test")
		c.base.Host = "exa\x7fmple.test"
		_, err := c.FetchCard(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid URL escape")
	})

	t.Run("honours the configured timeout", func(t *testing.T) {
		block := make(chan struct{})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-block
			_ = json.NewEncoder(w).Encode(a2a.CapabilityCard{}) //nolint:errcheck // test writer
		}))
		t.Cleanup(ts.Close)
		t.Cleanup(func() { close(block) })

		c := newClient(t, ts.URL, func(cfg *Config) { cfg.Timeout = 50 * time.Millisecond })
		_, err := c.FetchCard(context.Background())
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

// ---- SubmitTask ------------------------------------------------------

func TestSubmitTask_Validation(t *testing.T) {
	c := newClient(t, "http://example.test")
	ch, err := c.SubmitTask(context.Background(), a2a.Task{FromAgent: "me"})
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.Contains(t, err.Error(), "requires prompt or skill_name")
}

func TestSubmitTask_UnbuildableRequestURL(t *testing.T) {
	// Same escaping trap as TestFetchCard: the POST is never issued.
	c := newClient(t, "http://example.test")
	c.base.Host = "exa\x7fmple.test"
	ch, err := c.SubmitTask(context.Background(), a2a.Task{Prompt: "p"})
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.Contains(t, err.Error(), "invalid URL escape")
}

func TestSubmitTask_HappyPath(t *testing.T) {
	var gotTask a2a.Task
	var gotContentType, gotAccept, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tasks":
			gotContentType = r.Header.Get("Content-Type")
			gotAuth = r.Header.Get("Authorization")
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotTask))
			writeAck(w, "srv-task-1")
		case r.URL.Path == "/tasks/srv-task-1/events":
			gotAccept = r.Header.Get("Accept")
			sseHeaders(w)
			sse(t, w, a2a.TaskUpdate{TaskID: "srv-task-1", Status: a2a.TaskStatusRunning, Progress: 0.5})
			sse(t, w, a2a.TaskUpdate{TaskID: "srv-task-1", Status: a2a.TaskStatusCompleted, OutputText: "all done"})
			// Deliberately keep the stream open: the client must stop on
			// the terminal frame, not on EOF.
			time.Sleep(200 * time.Millisecond)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	c := newClient(t, ts.URL, func(cfg *Config) { cfg.AuthHeader = "Bearer tok" })
	ch, err := c.SubmitTask(context.Background(), a2a.Task{
		Prompt:         "do it",
		FromAgent:      "me",
		InputArtifacts: []a2a.Artifact{{URI: "artifact://1"}},
	})
	require.NoError(t, err)

	got := drain(t, ch)
	require.Len(t, got, 2)
	assert.Equal(t, a2a.TaskStatusRunning, got[0].Status)
	assert.InDelta(t, 0.5, got[0].Progress, 1e-9)
	assert.Equal(t, a2a.TaskStatusCompleted, got[1].Status)
	assert.Equal(t, "all done", got[1].OutputText)

	assert.Equal(t, "do it", gotTask.Prompt)
	assert.Equal(t, "me", gotTask.FromAgent)
	require.Len(t, gotTask.InputArtifacts, 1)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "Bearer tok", gotAuth)
	assert.Equal(t, "text/event-stream", gotAccept)
}

func TestSubmitTask_SkillOnlyIsAccepted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeAck(w, "t1")
			return
		}
		sseHeaders(w)
		sse(t, w, a2a.TaskUpdate{TaskID: "t1", Status: a2a.TaskStatusFailed, Message: "nope", FailureCode: "x"})
	}))
	defer ts.Close()

	ch, err := newClient(t, ts.URL).SubmitTask(context.Background(), a2a.Task{SkillName: "review"})
	require.NoError(t, err)
	got := drain(t, ch)
	require.Len(t, got, 1)
	assert.Equal(t, a2a.TaskStatusFailed, got[0].Status)
	assert.Equal(t, "x", got[0].FailureCode)
}

func TestSubmitTask_PostErrors(t *testing.T) {
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		wantErr  string
		closeSrv bool
	}{
		{
			name: "non-202 response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"task must set prompt or skill_name"}`) //nolint:errcheck // test writer
			},
			wantErr: "HTTP 400",
		},
		{
			name: "malformed ack",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, "{{{") //nolint:errcheck // test writer
			},
			wantErr: "decode task ack",
		},
		{
			name: "ack without a task_id",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{"status":"running"}`) //nolint:errcheck // test writer
			},
			wantErr: "server did not return task_id",
		},
		{
			name:     "transport failure",
			handler:  func(http.ResponseWriter, *http.Request) {},
			wantErr:  "",
			closeSrv: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.handler)
			base := ts.URL
			if tc.closeSrv {
				ts.Close()
			} else {
				defer ts.Close()
			}

			ch, err := newClient(t, base).SubmitTask(context.Background(), a2a.Task{Prompt: "p"})
			require.Error(t, err)
			assert.Nil(t, ch)
			if tc.wantErr != "" {
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestSubmitTask_StreamOpenErrors(t *testing.T) {
	t.Run("event stream returns non-200", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				writeAck(w, "t1")
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"unknown task_id"}`) //nolint:errcheck // test writer
		}))
		defer ts.Close()

		ch, err := newClient(t, ts.URL).SubmitTask(context.Background(), a2a.Task{Prompt: "p"})
		require.Error(t, err)
		assert.Nil(t, ch)
		assert.Contains(t, err.Error(), "HTTP 404")
		assert.Contains(t, err.Error(), "unknown task_id")
	})

	t.Run("event stream transport failure", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeAck(w, "t1")
		}))
		defer ts.Close()

		// Let the POST through but break the SSE GET.
		base := http.DefaultTransport
		hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/events") {
				return nil, errors.New("dial refused")
			}
			return base.RoundTrip(r)
		})}

		ch, err := newClient(t, ts.URL, func(cfg *Config) { cfg.HTTPClient = hc }).
			SubmitTask(context.Background(), a2a.Task{Prompt: "p"})
		require.Error(t, err)
		assert.Nil(t, ch)
		assert.Contains(t, err.Error(), "dial refused")
	})

	t.Run("server returns an unusable task_id", func(t *testing.T) {
		// A control byte in the id makes the stream URL unparseable.
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeAck(w, "bad\x7fid")
		}))
		defer ts.Close()

		ch, err := newClient(t, ts.URL).SubmitTask(context.Background(), a2a.Task{Prompt: "p"})
		require.Error(t, err)
		assert.Nil(t, ch)
		assert.Contains(t, err.Error(), "control character")
	})
}

func TestSubmitTask_StreamParsing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeAck(w, "t1")
			return
		}
		sseHeaders(w)
		// Noise the client must ignore.
		_, _ = io.WriteString(w, ": keep-alive comment\n\n")   //nolint:errcheck // test writer
		_, _ = io.WriteString(w, "event: ping\n\n")            //nolint:errcheck // test writer
		_, _ = io.WriteString(w, "data: {not-valid-json}\n\n") //nolint:errcheck // test writer
		w.(http.Flusher).Flush()
		sse(t, w, a2a.TaskUpdate{TaskID: "t1", Status: a2a.TaskStatusRunning, Message: "real"})
		sse(t, w, a2a.TaskUpdate{TaskID: "t1", Status: a2a.TaskStatusCancelled})
	}))
	defer ts.Close()

	ch, err := newClient(t, ts.URL).SubmitTask(context.Background(), a2a.Task{Prompt: "p"})
	require.NoError(t, err)
	got := drain(t, ch)
	require.Len(t, got, 2)
	assert.Equal(t, "real", got[0].Message)
	assert.Equal(t, a2a.TaskStatusCancelled, got[1].Status)
}

func TestSubmitTask_StreamEndsWithoutTerminalUpdate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeAck(w, "t1")
			return
		}
		sseHeaders(w)
		sse(t, w, a2a.TaskUpdate{TaskID: "t1", Status: a2a.TaskStatusRunning})
		// Handler returns: the body closes with no terminal frame.
	}))
	defer ts.Close()

	ch, err := newClient(t, ts.URL).SubmitTask(context.Background(), a2a.Task{Prompt: "p"})
	require.NoError(t, err)
	got := drain(t, ch)
	require.Len(t, got, 1)
	assert.Equal(t, a2a.TaskStatusRunning, got[0].Status)
}

func TestSubmitTask_ContextCancelStopsTheStream(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeAck(w, "t1")
			return
		}
		sseHeaders(w)
		// Emit far more than the 16-slot client buffer so the fan-out
		// goroutine ends up blocked on a send when ctx is cancelled.
		for i := 0; i < 64; i++ {
			blob, _ := json.Marshal(a2a.TaskUpdate{TaskID: "t1", Status: a2a.TaskStatusRunning, Progress: float64(i)}) //nolint:errcheck // test setup
			if _, err := fmt.Fprintf(w, "data: %s\n\n", blob); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := newClient(t, ts.URL).SubmitTask(ctx, a2a.Task{Prompt: "p"})
	require.NoError(t, err)

	// Wait until the client-side buffer is saturated, i.e. the goroutine
	// is parked on the send, then cancel.
	deadline := time.Now().Add(5 * time.Second)
	for len(ch) < cap(ch) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	require.Equal(t, cap(ch), len(ch), "client buffer never filled")
	cancel()

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for range ch {
		}
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("update channel was not closed after context cancel")
	}
	once.Do(func() { close(release) })
}

// ---- Cancel ----------------------------------------------------------

func TestCancel(t *testing.T) {
	t.Run("empty task id", func(t *testing.T) {
		err := newClient(t, "http://example.test").Cancel(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "taskID is required")
	})

	for _, code := range []int{http.StatusAccepted, http.StatusOK} {
		t.Run(fmt.Sprintf("status %d is accepted", code), func(t *testing.T) {
			var gotPath, gotMethod, gotAuth string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
				w.WriteHeader(code)
			}))
			defer ts.Close()

			c := newClient(t, ts.URL, func(cfg *Config) { cfg.AuthHeader = "Bearer tok" })
			require.NoError(t, c.Cancel(context.Background(), "task-9"))
			assert.Equal(t, "/tasks/task-9/cancel", gotPath)
			assert.Equal(t, http.MethodPost, gotMethod)
			assert.Equal(t, "Bearer tok", gotAuth)
		})
	}

	t.Run("unexpected status", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"unknown task_id"}`) //nolint:errcheck // test writer
		}))
		defer ts.Close()

		err := newClient(t, ts.URL).Cancel(context.Background(), "ghost")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 404")
		assert.Contains(t, err.Error(), "unknown task_id")
	})

	t.Run("transport error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		base := ts.URL
		ts.Close()

		err := newClient(t, base).Cancel(context.Background(), "t1")
		require.Error(t, err)
	})

	t.Run("unparseable task id", func(t *testing.T) {
		err := newClient(t, "http://example.test").Cancel(context.Background(), "bad\x7fid")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "control character")
	})
}

// ---- helpers under test ---------------------------------------------

func TestNewRequest_ResolvesRelativeToTheEndpointPath(t *testing.T) {
	c := newClient(t, "http://example.test/agents/peer/")
	req, err := c.newRequest(context.Background(), http.MethodGet, "tasks", nil)
	require.NoError(t, err)
	assert.Equal(t, "http://example.test/agents/peer/tasks", req.URL.String())
}

func TestUnexpectedStatus(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want string
	}{
		{name: "trims whitespace", code: 500, body: "  boom  \n", want: "a2a/client: HTTP 500: boom"},
		{name: "empty body", code: 404, body: "", want: "a2a/client: HTTP 404: "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(tc.body))}
			assert.EqualError(t, unexpectedStatus(resp), tc.want)
		})
	}

	t.Run("body read failure still reports the status", func(t *testing.T) {
		resp := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(errReader{})}
		assert.Contains(t, unexpectedStatus(resp).Error(), "HTTP 502")
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		status a2a.TaskStatus
		want   bool
	}{
		{a2a.TaskStatusCompleted, true},
		{a2a.TaskStatusFailed, true},
		{a2a.TaskStatusCancelled, true},
		{a2a.TaskStatusRunning, false},
		{a2a.TaskStatus(""), false},
		{a2a.TaskStatus("nonsense"), false},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			assert.Equal(t, tc.want, isTerminal(tc.status))
		})
	}
}
