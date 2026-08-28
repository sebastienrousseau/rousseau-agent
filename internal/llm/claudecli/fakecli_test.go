package claudecli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// fakeCLI is a /bin/sh stand-in for the `claude` binary. It records the
// exact argv it was invoked with, replays a canned stdout/stderr, and
// exits with a chosen status. Streaming cannot be exercised through the
// p.run seam (Stream drives exec.Cmd directly for its stdout pipe), so a
// real child process is required.
type fakeCLI struct {
	path     string
	argvFile string
}

func newFakeCLI(t *testing.T, stdout, stderr string, exitCode int) *fakeCLI {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	dir := t.TempDir()
	outFile := filepath.Join(dir, "stdout")
	errFile := filepath.Join(dir, "stderr")
	argvFile := filepath.Join(dir, "argv")
	require.NoError(t, os.WriteFile(outFile, []byte(stdout), 0o600))
	require.NoError(t, os.WriteFile(errFile, []byte(stderr), 0o600))

	script := fmt.Sprintf(`#!/bin/sh
for a in "$@"; do printf '%%s\n' "$a" >> %q; done
cat %q
cat %q >&2
exit %d
`, argvFile, outFile, errFile, exitCode)

	bin := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700)) //nolint:gosec // deliberately executable test fixture
	return &fakeCLI{path: bin, argvFile: argvFile}
}

// argv returns the arguments the fake binary observed, in order.
func (f *fakeCLI) argv(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(f.argvFile)
	require.NoError(t, err, "fake CLI was never invoked")
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// ndjson joins stream lines into the NDJSON the CLI emits under
// --output-format stream-json.
func ndjson(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

const resultLine = `{"type":"result","subtype":"success","result":"final answer",` +
	`"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":4}}`

// collect drains a Stream call.
func collect(t *testing.T, evs <-chan agent.StreamEvent, rep <-chan agent.StreamReport) ([]agent.StreamEvent, agent.StreamReport) {
	t.Helper()
	var got []agent.StreamEvent
	for e := range evs {
		got = append(got, e)
	}
	report, ok := <-rep
	require.True(t, ok, "report channel closed without a value")
	return got, report
}

// TestStream_HappyPathArgvAndEvents pins the full streaming contract:
// the exact argv handed to the CLI, the event sequence, the assembled
// Response, and the session bookkeeping.
func TestStream_HappyPathArgvAndEvents(t *testing.T) {
	cli := newFakeCLI(t, ndjson(
		`{"type":"system","subtype":"init","session_id":"sess-1"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"fin"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"al"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		resultLine,
	), "", 0)

	p := New(Config{
		Binary:         cli.path,
		Model:          "claude-opus-4-6",
		PermissionMode: "acceptEdits",
		ExtraArgs:      []string{"--add-dir", "/srv"},
	})

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: "sess-1",
		System:    "be terse",
		Messages:  []agent.Message{agent.NewUserText("hello there")},
	})
	require.NoError(t, err)
	got, report := collect(t, evs, rep)
	require.NoError(t, report.Err)

	assert.Equal(t, []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--session-id", "sess-1",
		"--system-prompt", "be terse",
		"--model", "claude-opus-4-6",
		"--permission-mode", "acceptEdits",
		"--add-dir", "/srv",
		"hello there",
	}, cli.argv(t))

	var kinds []agent.StreamEventKind
	var text strings.Builder
	for _, e := range got {
		kinds = append(kinds, e.Kind)
		text.WriteString(e.Delta)
	}
	assert.Equal(t, []agent.StreamEventKind{
		agent.StreamStart,
		agent.StreamTextDelta,
		agent.StreamTextDelta,
		agent.StreamOther, // the user/tool_result turn
		agent.StreamResult,
	}, kinds)
	assert.Equal(t, "final", text.String())

	require.Len(t, report.Response.Message.Content, 1)
	assert.Equal(t, "final answer", report.Response.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, report.Response.StopReason)
	assert.Equal(t, 12, report.Response.Usage.InputTokens)
	assert.Equal(t, 4, report.Response.Usage.OutputTokens)

	assert.True(t, p.knowsSession("sess-1"), "a clean stream must prime the session cache")
}

// TestStream_KnownSessionResumes proves the cache decides --resume vs
// --session-id for the streaming path too.
func TestStream_KnownSessionResumes(t *testing.T) {
	cli := newFakeCLI(t, ndjson(resultLine), "", 0)
	p := New(Config{Binary: cli.path}).WithCache(&stubCache{known: map[string]bool{"sess-7": true}})

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: "sess-7",
		Messages:  []agent.Message{agent.NewUserText("again")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.NoError(t, report.Err)

	argv := cli.argv(t)
	assert.Contains(t, argv, "--resume")
	assert.NotContains(t, argv, "--session-id")
}

// TestStream_NoSessionOmitsSessionFlags: a session-less request must not
// pass a bare --session-id with an empty value.
func TestStream_NoSessionOmitsSessionFlags(t *testing.T) {
	cli := newFakeCLI(t, ndjson(resultLine), "", 0)
	p := New(Config{Binary: cli.path})

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("one shot")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.NoError(t, report.Err)

	assert.Equal(t, []string{
		"--print", "--output-format", "stream-json", "--verbose", "one shot",
	}, cli.argv(t))
}

// TestStream_NonZeroExitSurfacesStderr: the CLI produced a parseable
// result yet exited non-zero, so the stderr tail is reported.
func TestStream_NonZeroExitSurfacesStderr(t *testing.T) {
	cli := newFakeCLI(t, ndjson(resultLine), "fatal: credit balance too low\n", 3)
	p := New(Config{Binary: cli.path})

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: "sess-x",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.Error(t, report.Err)
	assert.Contains(t, report.Err.Error(), "claudecli: stream exit")
	assert.Contains(t, report.Err.Error(), "exit status 3")
	assert.Contains(t, report.Err.Error(), "credit balance too low")
	assert.False(t, p.knowsSession("sess-x"), "a failed stream must not prime the cache")
}

// TestStream_ParseErrorWinsOverExitStatus: when the stream never yields
// a result line the parse error is reported, not the exit status.
func TestStream_ParseErrorWinsOverExitStatus(t *testing.T) {
	cli := newFakeCLI(t, "Claude configuration file not found\n", "boom\n", 1)
	p := New(Config{Binary: cli.path})

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: "sess-y",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.Error(t, report.Err)
	assert.Contains(t, report.Err.Error(), "ended without a result line")
	assert.NotContains(t, report.Err.Error(), "stream exit")
	assert.False(t, p.knowsSession("sess-y"))
}

// TestStream_ImagesBecomeImageFlags checks image temp files are passed
// as repeated --image flags, immediately before the prompt.
func TestStream_ImagesBecomeImageFlags(t *testing.T) {
	cli := newFakeCLI(t, ndjson(resultLine), "", 0)
	p := New(Config{Binary: cli.path})

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{
				{Kind: agent.ContentText, Text: "what are these"},
				{Kind: agent.ContentImage, Image: &agent.Image{MediaType: "image/png", Data: []byte{1}}},
				{Kind: agent.ContentImage, Image: &agent.Image{MediaType: "image/webp", Data: []byte{2}}},
			},
		}},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.NoError(t, report.Err)

	argv := cli.argv(t)
	require.Len(t, argv, 9)
	assert.Equal(t, "--image", argv[4])
	assert.True(t, strings.HasSuffix(argv[5], "img-0.png"), argv[5])
	assert.Equal(t, "--image", argv[6])
	assert.True(t, strings.HasSuffix(argv[7], "img-1.webp"), argv[7])
	assert.Equal(t, "what are these", argv[len(argv)-1])
}

func TestStream_NoUserContentFailsBeforeExec(t *testing.T) {
	p := New(Config{Binary: "/nonexistent-claude-binary"})
	evs, rep, err := p.Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewAssistantText("only assistant")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no user content")
	assert.Nil(t, evs)
	assert.Nil(t, rep)
}

func TestStream_ImageTempFileFailureFailsBeforeExec(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does", "not", "exist"))
	p := New(Config{Binary: "/nonexistent-claude-binary"})
	evs, rep, err := p.Stream(context.Background(), agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{{Kind: agent.ContentImage, Image: &agent.Image{
				MediaType: "image/png", Data: []byte{1},
			}}},
		}},
	})
	require.Error(t, err)
	assert.Nil(t, evs)
	assert.Nil(t, rep)
}

func TestStream_StartFailureIsSynchronous(t *testing.T) {
	p := New(Config{Binary: filepath.Join(t.TempDir(), "not-installed")})
	evs, rep, err := p.Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claudecli: start")
	assert.Nil(t, evs)
	assert.Nil(t, rep)
}

// TestStream_ContextCancellationKillsChild proves ctx cancellation
// terminates the subprocess rather than hanging the report goroutine.
func TestStream_ContextCancellationKillsChild(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o700)) //nolint:gosec // executable test fixture

	ctx, cancel := context.WithCancel(context.Background())
	p := New(Config{Binary: bin})
	evs, rep, err := p.Stream(ctx, agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	cancel()
	_, report := collect(t, evs, rep)
	require.Error(t, report.Err)
	assert.Contains(t, report.Err.Error(), "ended without a result line")
}

// -- Complete: retry + image paths -------------------------------------

// TestComplete_ResumeMissRetriesWithSessionID covers the symmetric
// cache miss: our cache believes the session exists but claude's store
// has been reset, so the provider re-creates it with --session-id.
func TestComplete_ResumeMissRetriesWithSessionID(t *testing.T) {
	p := New(Config{}).WithCache(&stubCache{known: map[string]bool{"sess-R": true}})
	var calls [][]string
	p.run = func(cmd *exec.Cmd) ([]byte, error) {
		calls = append(calls, cmd.Args)
		if contains(cmd.Args, "--resume") {
			return []byte("No conversation found with session ID: sess-R"),
				fmt.Errorf("exit status 1")
		}
		return []byte(`{"type":"result","result":"recreated","stop_reason":"end_turn"}`), nil
	}

	resp, err := p.Complete(context.Background(), agent.Request{
		SessionID: "sess-R",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	assert.Equal(t, "recreated", resp.Message.Content[0].Text)

	require.Len(t, calls, 2, "expected a --resume attempt then a --session-id retry")
	assert.Contains(t, calls[0], "--resume")
	assert.Contains(t, calls[1], "--session-id")
	assert.NotContains(t, calls[1], "--resume")
}

// TestComplete_ResumeMissNotRetriedForOtherErrors keeps the retry
// narrowly scoped to the "No conversation found" signal.
func TestComplete_ResumeMissNotRetriedForOtherErrors(t *testing.T) {
	p := New(Config{}).WithCache(&stubCache{known: map[string]bool{"sess-Q": true}})
	var calls int
	p.run = func(*exec.Cmd) ([]byte, error) {
		calls++
		return []byte("Error: model overloaded"), fmt.Errorf("exit status 1")
	}
	_, err := p.Complete(context.Background(), agent.Request{
		SessionID: "sess-Q",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
}

// TestComplete_ImageFlagsAndTempFiles verifies each image is written to
// a real file that exists while the CLI runs, and is removed after.
func TestComplete_ImageFlagsAndTempFiles(t *testing.T) {
	p := New(Config{})
	var imagePaths []string
	var contents [][]byte
	p.run = func(cmd *exec.Cmd) ([]byte, error) {
		for i, a := range cmd.Args {
			if a == "--image" {
				path := cmd.Args[i+1]
				imagePaths = append(imagePaths, path)
				data, err := os.ReadFile(path) //nolint:gosec // path produced by the code under test
				require.NoError(t, err, "image file must exist while the CLI runs")
				contents = append(contents, data)
			}
		}
		assert.Equal(t, "describe", cmd.Args[len(cmd.Args)-1], "prompt must be the final argv entry")
		return []byte(`{"type":"result","result":"ok","stop_reason":"end_turn"}`), nil
	}

	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{
				{Kind: agent.ContentText, Text: "describe"},
				{Kind: agent.ContentImage, Image: &agent.Image{MediaType: "image/jpeg", Data: []byte("JPEGDATA")}},
				{Kind: agent.ContentImage, Image: &agent.Image{MediaType: "image/gif", Data: []byte("GIFDATA")}},
				{Kind: agent.ContentImage, Image: nil}, // dropped
			},
		}},
	})
	require.NoError(t, err)

	require.Len(t, imagePaths, 2)
	assert.Equal(t, []byte("JPEGDATA"), contents[0])
	assert.Equal(t, []byte("GIFDATA"), contents[1])
	assert.True(t, strings.HasSuffix(imagePaths[0], "img-0.jpg"))
	assert.True(t, strings.HasSuffix(imagePaths[1], "img-1.gif"))

	for _, path := range imagePaths {
		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr), "temp image %s must be cleaned up", path)
	}
}

// TestComplete_ImageTempDirFailure covers the writeImages error branch
// of Complete: an unusable TMPDIR aborts before the CLI is invoked.
func TestComplete_ImageTempDirFailure(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing-dir"))
	p := New(Config{})
	p.run = func(*exec.Cmd) ([]byte, error) {
		t.Error("CLI must not be invoked when the image temp dir cannot be created")
		return nil, nil
	}
	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{{Kind: agent.ContentImage, Image: &agent.Image{
				MediaType: "image/png", Data: []byte{1},
			}}},
		}},
	})
	require.Error(t, err)
}

func TestLastUserContent_ImageOnlyTurnIsAllowed(t *testing.T) {
	img := &agent.Image{MediaType: "image/png", Data: []byte{1, 2, 3}}
	text, images, err := lastUserContent([]agent.Message{{
		Role: agent.RoleUser,
		Content: []agent.Content{
			{Kind: agent.ContentText, Text: ""},
			{Kind: agent.ContentImage, Image: img},
			{Kind: agent.ContentImage, Image: nil},
		},
	}})
	require.NoError(t, err)
	assert.Empty(t, text)
	require.Len(t, images, 1)
	assert.Same(t, img, images[0])
}

func TestWriteImages_TempDirCreationFailure(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "no", "such", "dir"))
	paths, cleanup, err := writeImages([]*agent.Image{{MediaType: "image/png", Data: []byte{1}}})
	require.Error(t, err)
	assert.Nil(t, paths)
	require.NotNil(t, cleanup)
	cleanup() // must be safe to call on the failure path
}

// TestWriteImages_FileWriteFailure drives the per-image os.WriteFile
// error branch. The temp dir is created just under PATH_MAX so MkdirTemp
// succeeds but appending "/img-0.png" pushes the file past the limit and
// the write fails with ENAMETOOLONG. MkdirTemp's random suffix varies in
// width, so retry a few times to land inside the window.
func TestWriteImages_FileWriteFailure(t *testing.T) {
	const pathMax = 4096
	base := t.TempDir()
	// Target a TMPDIR length such that
	//   len(TMPDIR) + len("/rousseau-cli-imgs-") + len(random) <= 4095
	// while that + len("/img-0.png") > 4095.
	const wantLen = 4062
	dir := base
	for len(dir) < wantLen {
		remaining := wantLen - len(dir) - 1
		if remaining <= 0 {
			break
		}
		if remaining > 200 {
			remaining = 200
		}
		dir = filepath.Join(dir, strings.Repeat("d", remaining))
	}
	if len(dir) != wantLen || len(dir) >= pathMax {
		t.Skipf("could not build a %d-byte temp path (got %d)", wantLen, len(dir))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Skipf("filesystem rejected the deep path: %v", err)
	}
	t.Setenv("TMPDIR", dir)

	var err error
	for attempt := 0; attempt < 12; attempt++ {
		var cleanup func()
		_, cleanup, err = writeImages([]*agent.Image{{MediaType: "image/png", Data: []byte{1}}})
		if cleanup != nil {
			cleanup()
		}
		if err != nil {
			break
		}
	}
	require.Error(t, err, "expected the image write to fail past PATH_MAX")
	assert.Contains(t, strings.ToLower(err.Error()), "too long")
}

// -- stream parsing edge cases ----------------------------------------

func TestClassifyLine_Table(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		kind     agent.StreamEventKind
		delta    string
		isResult bool
	}{
		{
			name: "assistant with a bare text_delta envelope",
			line: `{"type":"assistant","delta":{"type":"text_delta","text":"chunk"}}`,
			kind: agent.StreamTextDelta, delta: "chunk",
		},
		{
			name: "assistant delta of a non-text kind",
			line: `{"type":"assistant","delta":{"type":"input_json_delta","text":""}}`,
			kind: agent.StreamOther,
		},
		{
			name: "assistant with a non-array content field",
			line: `{"type":"assistant","message":{"content":42}}`,
			kind: agent.StreamOther,
		},
		{
			name: "assistant whose blocks are neither text nor tool_use",
			line: `{"type":"assistant","message":{"content":[{"type":"thinking"}]}}`,
			kind: agent.StreamOther,
		},
		{
			name: "assistant with an empty text block",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":""}]}}`,
			kind: agent.StreamOther,
		},
		{
			name: "user turn",
			line: `{"type":"user","message":{"content":[]}}`,
			kind: agent.StreamOther,
		},
		{
			name: "result line the parser rejects",
			line: `{"type":"result","is_error":true,"result":"rate limited"}`,
			kind: agent.StreamOther,
		},
		{
			name: "result line carrying max_tokens",
			line: `{"type":"result","result":"cut","stop_reason":"max_tokens"}`,
			kind: agent.StreamResult, isResult: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, delta, final, isResult := classifyLine(json.RawMessage(tc.line))
			assert.Equal(t, tc.kind, kind)
			assert.Equal(t, tc.delta, delta)
			assert.Equal(t, tc.isResult, isResult)
			if tc.isResult {
				assert.Equal(t, agent.StopMaxTokens, final.StopReason)
			}
		})
	}
}

// errReader fails after handing back one complete line.
type errReader struct {
	head string
	err  error
	done bool
}

func (e *errReader) Read(p []byte) (int, error) {
	if !e.done {
		e.done = true
		n := copy(p, e.head)
		return n, nil
	}
	return 0, e.err
}

func TestParseStream_ReaderErrorSurfaces(t *testing.T) {
	events := make(chan agent.StreamEvent, 8)
	_, err := parseStream(&errReader{
		head: `{"type":"system"}` + "\n",
		err:  fmt.Errorf("pipe closed unexpectedly"),
	}, events)
	close(events)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claudecli: read stream")
	assert.Contains(t, err.Error(), "pipe closed unexpectedly")
	assert.Len(t, drainEvents(events), 1, "events seen before the error are still delivered")
}

func drainEvents(ch <-chan agent.StreamEvent) []agent.StreamEvent {
	var out []agent.StreamEvent
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func TestHasToolUse_MalformedContent(t *testing.T) {
	assert.False(t, hasToolUse(json.RawMessage(`{"content":"not-an-array"}`)))
	assert.False(t, hasToolUse(json.RawMessage(`{"content":[{"type":"text"}]}`)))
	assert.True(t, hasToolUse(json.RawMessage(`{"content":[{"type":"tool_use"}]}`)))
}

func TestExtractTextDelta_MalformedContent(t *testing.T) {
	_, ok := extractTextDelta(json.RawMessage(`{"content":true}`))
	assert.False(t, ok)
	_, ok = extractTextDelta(json.RawMessage(`{"content":[{"type":"text","text":""}]}`))
	assert.False(t, ok)
	got, ok := extractTextDelta(json.RawMessage(`{"content":[{"type":"text","text":"x"}]}`))
	assert.True(t, ok)
	assert.Equal(t, "x", got)
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
