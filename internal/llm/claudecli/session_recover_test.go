package claudecli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the session-in-use recovery path.
//
// The failure this file defends against: rousseau's cached
// jid_sessions mapping keeps pointing at a claude session id whose
// on-disk transcript the CLI has flagged as "already in use". Every
// subsequent WhatsApp turn tried the same id, got the same refusal,
// and the user saw a red-cross reply until an operator manually
// intervened.
//
// The recovery detects the specific error text, atomically rotates
// the offending .jsonl aside, and retries the stream once with the
// same session id (fresh transcript). These tests lock in every
// branch of that path.
//
// The tests use table-driven cases wherever the input space is
// enumerable (isSessionInUseError, sessionFilePath). Integration
// with Stream lives in fakecli_test.go (multi-attempt fake CLI).

// -- isSessionInUseError -----------------------------------------------

func TestIsSessionInUseError_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error is not session-in-use",
			err:  nil,
			want: false,
		},
		{
			name: "exact production text matches",
			err:  errors.New("claudecli: stream exit: exit status 1: Error: Session ID cd8bd7d0-2b21-4e7d-b53a-dbc2eb9047bf is already in use.\n"),
			want: true,
		},
		{
			name: "shorter form still matches",
			err:  errors.New("Error: Session ID abc is already in use."),
			want: true,
		},
		{
			name: "wrapped fmt.Errorf preserves text",
			err:  fmt.Errorf("router: turn: provider: %w", errors.New("Session ID x is already in use")),
			want: true,
		},
		{
			name: "unrelated in-use phrases do NOT match — port in use",
			err:  errors.New("address already in use"),
			want: false,
		},
		{
			name: "unrelated in-use phrases do NOT match — database in use",
			err:  errors.New("database db is already in use"),
			want: false,
		},
		{
			name: "session id mentioned but not paired with in-use phrase",
			err:  errors.New("Session ID x rejected by backend"),
			want: false,
		},
		{
			name: "phrase reordered — matcher requires both anchors, order-agnostic",
			err:  errors.New("is already in use — Session ID"),
			want: true,
		},
		{
			name: "empty error text is not session-in-use",
			err:  errors.New(""),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isSessionInUseError(tc.err))
		})
	}
}

// -- sessionFilePath ---------------------------------------------------

func TestSessionFilePath_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		home    string
		cwd     string
		session string
		want    string
	}{
		{
			name:    "typical container layout",
			home:    "/home/rousseau",
			cwd:     "/workspace",
			session: "aaa",
			want:    "/home/rousseau/.claude/projects/-workspace/aaa.jsonl",
		},
		{
			name:    "deep nested cwd — slashes become dashes",
			home:    "/home/seb",
			cwd:     "/home/seb/team-rousseau-workspace/repos",
			session: "s1",
			want:    "/home/seb/.claude/projects/-home-seb-team-rousseau-workspace-repos/s1.jsonl",
		},
		{
			name:    "root cwd hashes to a single dash",
			home:    "/root",
			cwd:     "/",
			session: "id",
			want:    "/root/.claude/projects/-/id.jsonl",
		},
		{
			name:    "cwd without leading slash still normalised",
			home:    "/root",
			cwd:     "workspace/sub",
			session: "id",
			want:    "/root/.claude/projects/-workspace-sub/id.jsonl",
		},
		{
			name:    "empty cwd is treated as root",
			home:    "/home/x",
			cwd:     "",
			session: "id",
			want:    "/home/x/.claude/projects/-/id.jsonl",
		},
		{
			name:    "empty home returns empty — recovery caller no-ops",
			home:    "",
			cwd:     "/anywhere",
			session: "id",
			want:    "",
		},
		{
			name:    "empty session id returns empty — recovery caller no-ops",
			home:    "/home/x",
			cwd:     "/anywhere",
			session: "",
			want:    "",
		},
		{
			name:    "session id preserved verbatim (uuids kept intact)",
			home:    "/h",
			cwd:     "/w",
			session: "cd8bd7d0-2b21-4e7d-b53a-dbc2eb9047bf",
			want:    "/h/.claude/projects/-w/cd8bd7d0-2b21-4e7d-b53a-dbc2eb9047bf.jsonl",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sessionFilePath(tc.home, tc.cwd, tc.session))
		})
	}
}

// sessionFilePathDefault is thin — verify only that it uses real env
// rather than fabricating a path. Best-effort: if UserHomeDir or
// Getwd fail on this host we skip.
func TestSessionFilePathDefault_MatchesRealEnvironment(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Skipf("no cwd: %v", err)
	}
	got := sessionFilePathDefault("session-xyz")
	want := sessionFilePath(home, cwd, "session-xyz")
	assert.Equal(t, want, got)
	assert.Contains(t, got, "session-xyz.jsonl")
}

// sessionFilePathFrom lets us drive the two env-lookup error paths
// (host with no HOME, deleted CWD) deterministically. Real os.
// UserHomeDir / os.Getwd rarely fail on a well-formed host, so
// covering these branches via the injected-lookup form is the only
// way to guarantee they stay exercised across refactors.
func TestSessionFilePathFrom_HomeLookupFailureReturnsEmpty(t *testing.T) {
	got := sessionFilePathFrom(
		"any",
		func() (string, error) { return "", errors.New("no home") },
		func() (string, error) { return "/wd", nil },
	)
	assert.Empty(t, got, "home lookup failure must skip the rotate — caller propagates original error")
}

func TestSessionFilePathFrom_CwdLookupFailureReturnsEmpty(t *testing.T) {
	got := sessionFilePathFrom(
		"any",
		func() (string, error) { return "/home", nil },
		func() (string, error) { return "", errors.New("cwd deleted") },
	)
	assert.Empty(t, got, "cwd lookup failure must skip the rotate — caller propagates original error")
}

func TestSessionFilePathFrom_BothLookupsSucceedComposes(t *testing.T) {
	got := sessionFilePathFrom(
		"my-session",
		func() (string, error) { return "/h", nil },
		func() (string, error) { return "/w", nil },
	)
	assert.Equal(t, "/h/.claude/projects/-w/my-session.jsonl", got)
}

// -- rotateSessionFile -------------------------------------------------

func TestRotateSessionFile_EmptyPathNoOps(t *testing.T) {
	rotated, err := rotateSessionFile("", fixedTime)
	assert.False(t, rotated)
	assert.NoError(t, err)
}

func TestRotateSessionFile_MissingFileReturnsFalseNil(t *testing.T) {
	dir := t.TempDir()
	rotated, err := rotateSessionFile(filepath.Join(dir, "does-not-exist.jsonl"), fixedTime)
	assert.False(t, rotated)
	assert.NoError(t, err, "missing file must not surface as an error — recovery caller proceeds regardless")
}

func TestRotateSessionFile_HappyPathMovesFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sess.jsonl")
	require.NoError(t, os.WriteFile(src, []byte("history"), 0o600))

	rotated, err := rotateSessionFile(src, fixedTime)
	require.NoError(t, err)
	assert.True(t, rotated)

	// Original is gone.
	_, statErr := os.Stat(src)
	assert.True(t, os.IsNotExist(statErr), "original file must be renamed away")

	// Rotated copy exists with the timestamp suffix.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one rotated file expected")
	rotatedName := entries[0].Name()
	assert.True(t, strings.HasPrefix(rotatedName, "sess.jsonl.rotated-"),
		"rotated filename must preserve original name + .rotated- prefix; got %q", rotatedName)

	// Content preserved verbatim.
	body, err := os.ReadFile(filepath.Join(dir, rotatedName))
	require.NoError(t, err)
	assert.Equal(t, "history", string(body))
}

func TestRotateSessionFile_TimestampFormatIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.jsonl")
	require.NoError(t, os.WriteFile(src, nil, 0o600))

	rotated, err := rotateSessionFile(src, fixedTime)
	require.NoError(t, err)
	require.True(t, rotated)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	// fixedTime returns 2026-09-06 07:00:00 UTC; format string in
	// rotateSessionFile is "20060102-150405.000000000".
	assert.Equal(t, "s.jsonl.rotated-20260906-070000.000000000", entries[0].Name())
}

func TestRotateSessionFile_RenameFailureBubbles(t *testing.T) {
	// A path that stat can read but rename cannot succeed on. On
	// most POSIX systems, /proc/1/status is readable but the parent
	// dir is not writable, so rename returns a wrapped error.
	if _, err := os.Stat("/proc/1/status"); err != nil {
		t.Skip("/proc not available")
	}
	rotated, err := rotateSessionFile("/proc/1/status", fixedTime)
	assert.False(t, rotated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rotate session file")
}

func TestRotateSessionFile_StatFailureBubbles(t *testing.T) {
	// A directory the caller cannot traverse. Skip if we happen to
	// run as root — root sees through mode 0000.
	if os.Geteuid() == 0 {
		t.Skip("root sees through mode 0000 dirs")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	require.NoError(t, os.Mkdir(blocked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) }) // restore for cleanup

	rotated, err := rotateSessionFile(filepath.Join(blocked, "sess.jsonl"), fixedTime)
	assert.False(t, rotated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stat session file")
}

// fixedTime is the deterministic clock used by the rotate tests so
// the rotated-name format assertion is stable across runs.
func fixedTime() time.Time {
	return time.Date(2026, 9, 6, 7, 0, 0, 0, time.UTC)
}
