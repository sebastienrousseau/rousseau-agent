package audio_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media/audio"
)

// The whisper.cpp backend shells out to an external binary. To keep
// the exec path hermetic (no real whisper build, no model weights, no
// audio hardware) the test binary doubles as a stand-in for
// whisper.cpp: TestMain notices ROUSSEAU_TEST_FAKE_WHISPER=1 and
// short-circuits *before* the testing package parses flags, mimics
// whisper.cpp's CLI contract, then exits.

const (
	fakeEnv     = "ROUSSEAU_TEST_FAKE_WHISPER"
	fakeModeEnv = "ROUSSEAU_TEST_FAKE_WHISPER_MODE"
	fakeArgsEnv = "ROUSSEAU_TEST_FAKE_WHISPER_ARGS"
	fakeTextEnv = "ROUSSEAU_TEST_FAKE_WHISPER_TEXT"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		os.Exit(runFakeWhisper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// runFakeWhisper imitates `whisper-cli -m MODEL -f INPUT -otxt …`.
// With -otxt the real binary writes its transcript to INPUT.txt; some
// builds only emit on stdout, which the "stdout" mode reproduces.
func runFakeWhisper(args []string) int {
	if p := os.Getenv(fakeArgsEnv); p != "" {
		_ = os.WriteFile(p, []byte(strings.Join(args, " ")), 0o600) //nolint:errcheck,gosec // test fixture
	}
	var input string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" {
			input = args[i+1]
		}
	}
	text := os.Getenv(fakeTextEnv)
	switch os.Getenv(fakeModeEnv) {
	case "fail":
		fmt.Fprintln(os.Stderr, "whisper: failed to load model")
		return 3
	case "stdout":
		fmt.Fprintln(os.Stdout, "  "+text+"  ")
		return 0
	case "empty-txt":
		// Zero-byte sidecar → the backend must fall back to stdout.
		_ = os.WriteFile(input+".txt", nil, 0o600) //nolint:errcheck,gosec // test fixture
		fmt.Fprintln(os.Stdout, text)
		return 0
	default:
		_ = os.WriteFile(input+".txt", []byte("\n  "+text+"  \n"), 0o600) //nolint:errcheck,gosec // test fixture
		return 0
	}
}

// fakeWhisper points WhisperCPP at this very test binary and returns
// the model-file path it should use.
func fakeWhisper(t *testing.T, mode, text string) (bin, model string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("exec-based whisper fixture assumes POSIX paths")
	}
	self, err := os.Executable()
	require.NoError(t, err)

	dir := t.TempDir()
	model = filepath.Join(dir, "ggml-base.en.bin")
	require.NoError(t, os.WriteFile(model, []byte("not-a-real-model"), 0o600))

	t.Setenv(fakeEnv, "1")
	t.Setenv(fakeModeEnv, mode)
	t.Setenv(fakeTextEnv, text)
	// Keep staged audio inside the test's own scratch space.
	t.Setenv("TMPDIR", dir)
	return self, model
}

func TestWhisperCPP_TranscribesFromSidecarTextFile(t *testing.T) {
	bin, model := fakeWhisper(t, "txt", "bonjour le monde")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv(fakeArgsEnv, argsFile)

	w := &audio.WhisperCPP{Binary: bin, ModelFile: model, Language: "fr"}
	res, err := w.Transcribe(context.Background(), []byte("OggS-fake-audio"), "audio/ogg; codecs=opus")
	require.NoError(t, err)

	assert.Equal(t, "bonjour le monde", res.Text)
	assert.Equal(t, "fr", res.Language)
	assert.Positive(t, res.Duration)

	raw, err := os.ReadFile(argsFile) //nolint:gosec // test-controlled path
	require.NoError(t, err)
	args := string(raw)
	assert.Contains(t, args, "-m "+model, "model must be passed through")
	assert.Contains(t, args, "-l fr", "language hint must be passed through")
	assert.Contains(t, args, "-otxt")
	assert.Contains(t, args, ".ogg", "staged audio should keep a mime-derived extension")
}

func TestWhisperCPP_OmitsLanguageFlagWhenUnset(t *testing.T) {
	bin, model := fakeWhisper(t, "txt", "hello")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv(fakeArgsEnv, argsFile)

	w := &audio.WhisperCPP{Binary: bin, ModelFile: model}
	res, err := w.Transcribe(context.Background(), []byte("audio"), "audio/wav")
	require.NoError(t, err)
	assert.Equal(t, "hello", res.Text)
	assert.Empty(t, res.Language)

	raw, err := os.ReadFile(argsFile) //nolint:gosec // test-controlled path
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "-l ")
}

func TestWhisperCPP_FallsBackToStdout(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{name: "no sidecar written", mode: "stdout"},
		{name: "empty sidecar", mode: "empty-txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bin, model := fakeWhisper(t, tc.mode, "stdout transcript")
			w := &audio.WhisperCPP{Binary: bin, ModelFile: model}
			res, err := w.Transcribe(context.Background(), []byte("audio"), "audio/mpeg")
			require.NoError(t, err)
			assert.Equal(t, "stdout transcript", res.Text)
		})
	}
}

func TestWhisperCPP_NonZeroExitSurfacesStderr(t *testing.T) {
	bin, model := fakeWhisper(t, "fail", "")
	w := &audio.WhisperCPP{Binary: bin, ModelFile: model}
	_, err := w.Transcribe(context.Background(), []byte("audio"), "audio/flac")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audio/whisper-cpp:")
	assert.Contains(t, err.Error(), "failed to load model")
}

func TestWhisperCPP_ResolvesBinaryFromPath(t *testing.T) {
	bin, model := fakeWhisper(t, "txt", "resolved via PATH")
	// Only "whisper" (the second candidate) exists, so the lookup
	// must skip "whisper-cli" and keep going.
	pathDir := t.TempDir()
	require.NoError(t, os.Symlink(bin, filepath.Join(pathDir, "whisper")))
	t.Setenv("PATH", pathDir)

	w := &audio.WhisperCPP{ModelFile: model}
	res, err := w.Transcribe(context.Background(), []byte("audio"), "audio/webm")
	require.NoError(t, err)
	assert.Equal(t, "resolved via PATH", res.Text)
}

func TestWhisperCPP_NoBinaryOnPathIsUnavailable(t *testing.T) {
	_, model := fakeWhisper(t, "txt", "")
	t.Setenv("PATH", t.TempDir()) // empty dir: no candidate resolves

	w := &audio.WhisperCPP{ModelFile: model}
	_, err := w.Transcribe(context.Background(), []byte("audio"), "audio/ogg")
	assert.ErrorIs(t, err, audio.ErrBackendUnavailable)
}

func TestWhisperCPP_TempFileCreationFailure(t *testing.T) {
	bin, model := fakeWhisper(t, "txt", "")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "no", "such", "dir"))

	w := &audio.WhisperCPP{Binary: bin, ModelFile: model}
	_, err := w.Transcribe(context.Background(), []byte("audio"), "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tempfile")
}

func TestWhisperCPP_TimeoutCancelsTheProcess(t *testing.T) {
	bin, model := fakeWhisper(t, "txt", "too late")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-dead context: exec must fail immediately

	w := &audio.WhisperCPP{Binary: bin, ModelFile: model, Timeout: time.Minute}
	_, err := w.Transcribe(ctx, []byte("audio"), "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audio/whisper-cpp:")
}

func TestExtensionForMime(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"audio/ogg", ".ogg"},
		{"audio/opus", ".ogg"},
		{"audio/ogg; codecs=opus", ".ogg"},
		{" AUDIO/OGG ; codecs=opus", ".ogg"},
		{"audio/mp4", ".m4a"},
		{"audio/x-m4a", ".m4a"},
		{"audio/aac", ".m4a"},
		{"audio/mpeg", ".mp3"},
		{"audio/mp3", ".mp3"},
		{"audio/wav", ".wav"},
		{"audio/x-wav", ".wav"},
		{"audio/wave", ".wav"},
		{"audio/webm", ".webm"},
		{"audio/flac", ".flac"},
		// Unknown types fall through to a best-effort extension guess.
		{"application/octet-stream", ""},
		{"weird/thing.bin", ".bin"},
	}
	for _, tc := range tests {
		t.Run(tc.mime, func(t *testing.T) {
			assert.Equal(t, tc.want, audio.ExtensionForMime(tc.mime))
		})
	}
}
