package claudecli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session-in-use recovery.
//
// The claude CLI refuses to reuse a session id when it thinks the
// on-disk session file is currently held ("Error: Session ID X is
// already in use."). Rousseau caches session ids per-sender for
// context continuity, so a false-positive from that check would
// otherwise wedge a WhatsApp chat: every subsequent turn tries the
// same cached id, gets rejected, and the user sees a red-cross
// reply forever.
//
// The recovery in this file:
//
//   1. Detect the specific error text (isSessionInUseError).
//   2. Atomically rotate the offending .jsonl aside so the next
//      claude subprocess sees no file and creates a fresh one
//      (rotateSessionFile). History is preserved as
//      <id>.jsonl.rotated-<ts> next to the original so a curious
//      operator can inspect / restore it.
//   3. Retry the stream once with the SAME session id. The rotated
//      file no longer collides, claude accepts --session-id X, the
//      caller's JID→session mapping keeps pointing to the same id.
//
// The recovery is one-shot per Stream call — if the retry itself
// hits the same error, we surface the original failure. That
// prevents an infinite retry loop if the claude CLI ever mutates
// the check to something we can't recover from.

// sessionInUseMarker is the exact substring the claude CLI emits on
// stderr when it refuses to open a session file it considers busy.
// Matched literally (case-insensitive would be false positives from
// unrelated "in use" strings elsewhere in the pipeline). If the CLI
// ever changes the wording, the recover path stops firing and the
// caller sees the raw error — same behaviour as pre-fix, so the
// change is failure-safe.
const sessionInUseMarker = "is already in use"

// isSessionInUseError reports whether err carries the claude CLI's
// "Session ID X is already in use" refusal. nil errs return false.
// Wrapping is tolerated: errors.Is is not used because the CLI text
// is opaque; we substring-match the surfaced .Error() string.
func isSessionInUseError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Both anchor phrases must appear — "is already in use" alone is
	// too permissive (matches address-in-use, database-in-use, etc.
	// that surface elsewhere in a stream_exit envelope).
	return strings.Contains(msg, "Session ID") && strings.Contains(msg, sessionInUseMarker)
}

// sessionFilePath returns the location of claude's session-transcript
// file for sessionID given the process's current working directory.
// The claude CLI hashes cwd by replacing each '/' with '-' and
// prefixing with '-'; the transcript lives at
// $HOME/.claude/projects/<hashed-cwd>/<id>.jsonl.
//
// If home cannot be resolved or the id is empty the function returns
// "" without an error so callers can no-op gracefully.
//
// homeDir and cwd are threaded in for tests; production callers use
// sessionFilePathDefault which supplies os.UserHomeDir + os.Getwd.
func sessionFilePath(homeDir, cwd, sessionID string) string {
	if homeDir == "" || sessionID == "" {
		return ""
	}
	if cwd == "" {
		cwd = "/"
	}
	// Match claude's project-directory naming exactly.
	hashed := "-" + strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-")
	return filepath.Join(homeDir, ".claude", "projects", hashed, sessionID+".jsonl")
}

// sessionFilePathDefault resolves the transcript path via the real
// environment. Returns "" when either lookup fails so the caller can
// skip the rotate and let the original error propagate.
func sessionFilePathDefault(sessionID string) string {
	return sessionFilePathFrom(sessionID, os.UserHomeDir, os.Getwd)
}

// sessionFilePathFrom is sessionFilePathDefault's inner form with
// the two env lookups injected. Extracted for tests: the real
// os.UserHomeDir / os.Getwd rarely fail on a well-formed host, so
// the error branches would otherwise be untestable without process-
// wide env manipulation (fragile and unfriendly to parallel tests).
func sessionFilePathFrom(sessionID string, homeFn func() (string, error), cwdFn func() (string, error)) string {
	home, err := homeFn()
	if err != nil {
		return ""
	}
	cwd, err := cwdFn()
	if err != nil {
		return ""
	}
	return sessionFilePath(home, cwd, sessionID)
}

// sessionFilePathResolver is the seam Stream calls to locate the
// transcript file when running the in-use recovery. Package-level
// var so integration tests can substitute a deterministic path
// pointing into a t.TempDir() without depending on $HOME layout.
// Production always uses sessionFilePathDefault.
var sessionFilePathResolver = sessionFilePathDefault

// rotateSessionFile atomically renames the transcript for sessionID
// out of the way so claude sees a fresh slate on the next invocation.
// Returns (true, nil) when the file existed and was rotated,
// (false, nil) when there was no file to rotate (recovery still
// makes sense — claude may have released the reservation), and
// (false, err) only when rename itself failed (permission, cross-
// device, etc). The timestamp suffix is deterministic per call via
// now() so tests can substitute a fixed clock.
//
// rotate uses os.Rename rather than os.Remove so the transcript is
// preserved — anything writing to the old inode continues to write
// there (Linux rename semantics), and an operator inspecting
// ~/.claude/projects/<hash>/ sees the rotated file next to any new
// one.
func rotateSessionFile(path string, now func() time.Time) (bool, error) {
	if path == "" {
		return false, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("claudecli: stat session file %q: %w", path, err)
	}
	stamp := now().UTC().Format("20060102-150405.000000000")
	rotated := path + ".rotated-" + stamp
	if err := os.Rename(path, rotated); err != nil {
		return false, fmt.Errorf("claudecli: rotate session file %q: %w", path, err)
	}
	return true, nil
}
