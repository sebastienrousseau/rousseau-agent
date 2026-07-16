//go:build no_whatsmeow

package whatsapp

import "context"

// WhisperConfig mirrors the standard build's shape so cli/whatsapp.go
// compiles under -tags=no_whatsmeow. The Whisper transcriber itself
// works via an external CLI, so it does not need whatsmeow — but the
// dispatch pipeline that would consume it does. Keeping the constructor
// present avoids a downstream ripple in the CLI wiring.
type WhisperConfig struct {
	Binary    string
	Model     string
	ModelPath string
	Language  string
	ExtraArgs []string
}

// WhisperTranscriber is a stub. Its Transcribe always returns
// errCompiledOut.
type WhisperTranscriber struct{}

// NewWhisperTranscriber constructs a stub transcriber under the lite
// build.
func NewWhisperTranscriber(_ WhisperConfig) *WhisperTranscriber {
	return &WhisperTranscriber{}
}

// Transcribe implements Transcriber. Always returns errCompiledOut.
func (*WhisperTranscriber) Transcribe(_ context.Context, _ []byte, _ string) (string, error) {
	return "", errCompiledOut
}
