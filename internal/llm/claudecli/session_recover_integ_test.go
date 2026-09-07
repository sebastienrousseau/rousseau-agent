package claudecli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Integration regression tests for the session-in-use recovery in
// Stream(). These wire the real subprocess pipeline (via a fake
// `claude` binary) end-to-end so the tests would also catch a
// refactor that dropped the retry hook, the cache Forget call, or
// the rotate seam.

// newMultiAttemptFakeCLI writes a /bin/sh script that behaves
// differently across successive invocations: attempt N reads
// attempts[N-1] from the two-element slice. attempts[0] is the
// FIRST invocation's fixture, attempts[1] is the SECOND's — anything
// beyond that reuses attempts[1]. The script tracks its call count
// via a counter file in the test's TempDir.
func newMultiAttemptFakeCLI(t *testing.T, attempts []attemptFixture) *fakeCLI {
	t.Helper()
	require.NotEmpty(t, attempts, "at least one attempt fixture required")
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	dir := t.TempDir()
	counterFile := filepath.Join(dir, "counter")
	argvFile := filepath.Join(dir, "argv")
	require.NoError(t, os.WriteFile(counterFile, []byte("0"), 0o600))

	// Write per-attempt stdout / stderr / exit into files the script
	// selects at runtime via the counter. Attempts beyond len clamp
	// to the last fixture — matches "retry uses attempt[1]" semantics.
	for i, a := range attempts {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("stdout-%d", i)), []byte(a.stdout), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("stderr-%d", i)), []byte(a.stderr), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("exit-%d", i)), []byte(fmt.Sprintf("%d", a.exit)), 0o600))
	}

	script := fmt.Sprintf(`#!/bin/sh
set -e
DIR=%q
CTR=$(cat "$DIR/counter")
NEXT=$((CTR + 1))
echo "$NEXT" > "$DIR/counter"
IDX=$CTR
MAX=%d
if [ "$IDX" -ge "$MAX" ]; then IDX=$((MAX - 1)); fi
for a in "$@"; do printf '%%s\n' "$a" >> %q; done
cat "$DIR/stdout-$IDX"
cat "$DIR/stderr-$IDX" >&2
exit $(cat "$DIR/exit-$IDX")
`, dir, len(attempts), argvFile)

	bin := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700)) //nolint:gosec // deliberately executable test fixture
	return &fakeCLI{path: bin, argvFile: argvFile}
}

type attemptFixture struct {
	stdout, stderr string
	exit           int
}

// swapSessionFilePathResolver substitutes the recovery's path
// resolver so it points into a test-controlled directory. Restores
// the production resolver via t.Cleanup.
func swapSessionFilePathResolver(t *testing.T, fn func(string) string) {
	t.Helper()
	prev := sessionFilePathResolver
	sessionFilePathResolver = fn
	t.Cleanup(func() { sessionFilePathResolver = prev })
}

// sessionInUseStderr is the exact stderr the real claude CLI writes
// when it refuses a session. Keeps the tests decoupled from the
// production error-detection regexp — if the detector regresses
// this shape is what will hit it in production.
const sessionInUseStderr = "Error: Session ID %s is already in use.\n"

// -- Recovery happy path -----------------------------------------------

// TestStream_SessionInUseRecovery_RotateAndRetrySucceeds locks in
// the primary fix: first attempt returns session-in-use, rotate
// moves the transcript aside, second attempt sees a fresh slate and
// succeeds. The Report surfaces the SECOND attempt's response, the
// cache is Forget'd (so post-recover the id is treated as fresh),
// and exactly one rotated file exists next to the (never re-created)
// original path.
func TestStream_SessionInUseRecovery_RotateAndRetrySucceeds(t *testing.T) {
	dir := t.TempDir()
	sessID := "sess-recover-happy"
	pathFor := func(id string) string {
		return filepath.Join(dir, id+".jsonl")
	}
	swapSessionFilePathResolver(t, pathFor)

	// Pre-create the "poisoned" transcript the recovery will rotate.
	require.NoError(t, os.WriteFile(pathFor(sessID), []byte("conversation history"), 0o600))

	cli := newMultiAttemptFakeCLI(t, []attemptFixture{
		{stderr: fmt.Sprintf(sessionInUseStderr, sessID), exit: 1},
		{stdout: ndjson(sampleResultLine("recovered")), exit: 0},
	})
	sc := &stubCache{}
	p := New(Config{Binary: cli.path}).WithCache(sc)

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: sessID,
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.NoError(t, report.Err, "recovery must return a clean report")
	require.Len(t, report.Response.Message.Content, 1)
	assert.Equal(t, "recovered", report.Response.Message.Content[0].Text)

	// The cache saw a Forget(id) so the retry uses --session-id
	// (fresh transcript) rather than --resume. After the successful
	// retry, the session is Remember'd again — that's fine, the id
	// is now valid.
	assert.Contains(t, sc.forgot, sessID, "cache must be Forget'd on recovery so --session-id fires on retry")
	assert.Contains(t, sc.remember, sessID, "successful retry must Remember the id")

	// Exactly one rotated file next to a NEW (or absent) canonical
	// file — the retry did not itself create a transcript because
	// the fake doesn't write jsonl, and we don't want the test to
	// depend on claude's file creation.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var rotatedCount int
	for _, e := range entries {
		if e.Name() == sessID+".jsonl" {
			t.Fatalf("original path %q must have been rotated away", e.Name())
		}
		if len(e.Name()) > len(sessID+".jsonl.rotated-") &&
			e.Name()[:len(sessID+".jsonl.rotated-")] == sessID+".jsonl.rotated-" {
			rotatedCount++
		}
	}
	assert.Equal(t, 1, rotatedCount, "exactly one rotated transcript expected")
}

// -- Retry limit (no infinite loop) ------------------------------------

// TestStream_SessionInUseRecovery_RetryFailureSurfacesSecondError
// verifies the one-shot semantics: if the retry ALSO hits
// session-in-use (or any other error), we surface that error and do
// not rotate/retry again. Prevents an infinite loop when claude has
// changed the meaning of the error text or the file-lock is not
// actually held on disk.
func TestStream_SessionInUseRecovery_RetryFailureSurfacesSecondError(t *testing.T) {
	dir := t.TempDir()
	sessID := "sess-recover-retry-fails"
	swapSessionFilePathResolver(t, func(id string) string { return filepath.Join(dir, id+".jsonl") })
	require.NoError(t, os.WriteFile(filepath.Join(dir, sessID+".jsonl"), []byte("x"), 0o600))

	cli := newMultiAttemptFakeCLI(t, []attemptFixture{
		{stderr: fmt.Sprintf(sessionInUseStderr, sessID), exit: 1},
		{stderr: "second attempt boom\n", exit: 2},
	})
	p := New(Config{Binary: cli.path}).WithCache(&stubCache{})

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: sessID,
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.Error(t, report.Err)
	assert.Contains(t, report.Err.Error(), "second attempt boom",
		"the SECOND attempt's error must surface, not a stale copy of the first")
	assert.Contains(t, report.Err.Error(), "exit status 2")
}

// TestStream_SessionInUseRecovery_ChainedInUseDoesNotLoop covers the
// exact pathological case: both attempts return session-in-use. The
// retry-once guarantee means the SECOND session-in-use surfaces and
// there is no third attempt. We assert via the fake's counter that
// the CLI ran exactly twice.
func TestStream_SessionInUseRecovery_ChainedInUseDoesNotLoop(t *testing.T) {
	dir := t.TempDir()
	sessID := "sess-recover-loop-guard"
	swapSessionFilePathResolver(t, func(id string) string { return filepath.Join(dir, id+".jsonl") })
	require.NoError(t, os.WriteFile(filepath.Join(dir, sessID+".jsonl"), []byte("x"), 0o600))

	cli := newMultiAttemptFakeCLI(t, []attemptFixture{
		{stderr: fmt.Sprintf(sessionInUseStderr, sessID), exit: 1},
		{stderr: fmt.Sprintf(sessionInUseStderr, sessID), exit: 1},
	})
	// The counter file lives in the same tempdir as the script.
	counterFile := filepath.Join(filepath.Dir(cli.path), "counter")

	p := New(Config{Binary: cli.path}).WithCache(&stubCache{})
	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: sessID,
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.Error(t, report.Err)
	assert.True(t, isSessionInUseError(report.Err), "final error must still be session-in-use")

	// Wait briefly to allow the goroutine's cleanup to finish
	// writing the counter (Stream's report goroutine is async).
	require.Eventually(t, func() bool {
		body, _ := os.ReadFile(counterFile) //nolint:errcheck // retried inside Eventually, missing file is a retry signal
		return strings.TrimSpace(string(body)) == "2"
	}, time.Second, 20*time.Millisecond, "fake CLI must be invoked exactly twice — no infinite retry loop")
}

// -- Rotate failure short-circuits the retry ---------------------------

// TestStream_SessionInUseRecovery_RotateFailureSurfacesOriginalError
// covers: when rotate itself fails (permission denied, cross-device,
// etc.), we do NOT retry — the original session-in-use error is what
// the caller sees. Rationale: retrying without rotating puts us right
// back into the same trap, potentially forever.
func TestStream_SessionInUseRecovery_RotateFailureSurfacesOriginalError(t *testing.T) {
	sessID := "sess-recover-rotate-fails"
	// Resolver returns a path in a directory that does not exist,
	// so stat succeeds only via ENOENT (returning rotated=false,
	// nil), which then means we DO NOT retry — we simply skip the
	// recovery and let the original error propagate. Slightly
	// different than a rename-EACCES failure, but exercises the
	// "path resolved to something rotate couldn't act on" branch.
	//
	// To make the retry NOT fire and thereby cover the "no retry
	// after rotate no-op'd" branch, we resolve to a path where the
	// file legitimately doesn't exist. rotateSessionFile returns
	// (false, nil); recovery falls through and issues the retry
	// anyway. So this test actually needs a rotate that RETURNS AN
	// ERROR — use a /proc file.
	if _, err := os.Stat("/proc/1/status"); err != nil {
		t.Skip("/proc not available")
	}
	swapSessionFilePathResolver(t, func(id string) string { return "/proc/1/status" })

	cli := newMultiAttemptFakeCLI(t, []attemptFixture{
		{stderr: fmt.Sprintf(sessionInUseStderr, sessID), exit: 1},
		// Second attempt would succeed if we retried — but rotate
		// failed so we must NOT retry.
		{stdout: ndjson(sampleResultLine("should not appear")), exit: 0},
	})
	counterFile := filepath.Join(filepath.Dir(cli.path), "counter")

	p := New(Config{Binary: cli.path}).WithCache(&stubCache{})
	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: sessID,
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.Error(t, report.Err)
	assert.True(t, isSessionInUseError(report.Err), "original session-in-use error must propagate when rotate fails")

	// Verify the CLI was NOT invoked a second time.
	require.Eventually(t, func() bool {
		body, _ := os.ReadFile(counterFile) //nolint:errcheck // retried inside Eventually, missing file is a retry signal
		return strings.TrimSpace(string(body)) == "1"
	}, time.Second, 20*time.Millisecond, "rotate failure must NOT trigger a retry — the CLI must run exactly once")
}

// -- Happy path (no in-use, no rotate) ---------------------------------

// TestStream_NoSessionInUseError_NoRecoveryFires is the negative
// sanity: a normal successful Stream call MUST NOT touch the
// resolver, MUST NOT rotate anything, MUST NOT Forget the cache.
// Guards against a refactor that accidentally always runs the
// recovery path.
func TestStream_NoSessionInUseError_NoRecoveryFires(t *testing.T) {
	resolverCalls := 0
	swapSessionFilePathResolver(t, func(id string) string {
		resolverCalls++
		return ""
	})

	cli := newFakeCLI(t, ndjson(sampleResultLine("ok")), "", 0)
	sc := &stubCache{}
	p := New(Config{Binary: cli.path}).WithCache(sc)

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: "sess-happy",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.NoError(t, report.Err)

	assert.Zero(t, resolverCalls, "resolver must not be consulted on the happy path")
	assert.Empty(t, sc.forgot, "cache must not be Forget'd on the happy path")
}

// TestStream_SessionInUseRecovery_RetryStartFailureSurfaces covers
// the "retry couldn't even launch" branch: rotate succeeded, but
// spawning the second subprocess failed (binary vanished, no exec
// permission, etc). The report must carry the wrapped "session
// recover: restart" error and the CLI counter must show exactly one
// SUCCESSFUL invocation (the second exec.Start returns before the
// script runs).
func TestStream_SessionInUseRecovery_RetryStartFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	sessID := "sess-recover-restart-fails"
	swapSessionFilePathResolver(t, func(id string) string { return filepath.Join(dir, id+".jsonl") })
	require.NoError(t, os.WriteFile(filepath.Join(dir, sessID+".jsonl"), []byte("x"), 0o600))

	// Script deletes itself after the first invocation so the
	// retry's cmd.Start() sees "no such file". A rare-in-prod but
	// covered branch.
	scriptDir := t.TempDir()
	binPath := filepath.Join(scriptDir, "claude")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s' "%s" >&2
rm -f %q
exit 1
`, fmt.Sprintf(sessionInUseStderr, sessID), binPath)
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o700)) //nolint:gosec // executable test fixture

	p := New(Config{Binary: binPath}).WithCache(&stubCache{})
	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: sessID,
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.Error(t, report.Err)
	assert.Contains(t, report.Err.Error(), "session recover: restart",
		"restart-failure branch must be surfaced as a distinct wrapped error")
	assert.Contains(t, report.Err.Error(), "claudecli: start",
		"underlying exec.Start error must be preserved via %w wrap")
}

// TestStream_SessionInUseWithoutSessionID_SkipsRecovery covers the
// edge where the error text matches but req.SessionID is empty
// (unusual — every rousseau request carries an id — but the guard
// exists to avoid resolving a path with an empty component). We
// verify the recovery does NOT fire.
func TestStream_SessionInUseWithoutSessionID_SkipsRecovery(t *testing.T) {
	resolverCalls := 0
	swapSessionFilePathResolver(t, func(id string) string {
		resolverCalls++
		return ""
	})

	cli := newFakeCLI(t, "", fmt.Sprintf(sessionInUseStderr, "phantom"), 1)
	p := New(Config{Binary: cli.path}).WithCache(&stubCache{})

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		// Deliberately no SessionID.
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.Error(t, report.Err, "error still surfaces")
	assert.Zero(t, resolverCalls, "no SessionID means no recovery — resolver must not be consulted")
}

// -- helper for constructing a `type:"result"` NDJSON line -------------

// sampleResultLine returns a cliResult JSON envelope carrying the
// given text. Keeps the recovery tests independent of the (larger,
// meant-for-real-invocations) result fixtures in fakecli_test.go.
func sampleResultLine(text string) string {
	return fmt.Sprintf(`{"type":"result","result":%q,"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`, text)
}
