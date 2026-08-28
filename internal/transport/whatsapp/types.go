// Package whatsapp — types defined here are shared across the
// standard (whatsmeow-backed) and no_whatsmeow (stub) builds, so
// this file carries no build tag and no whatsmeow imports.
package whatsapp

import "context"

// Transcriber converts an audio payload into text. Implementations are
// free to shell out (whisper.cpp), call a remote service, or return
// early — nil transcribers skip audio messages entirely.
type Transcriber interface {
	// Transcribe returns the plain-text transcription. mimetype is
	// informational; implementations may use it to pick a decoder or
	// hint the model.
	Transcribe(ctx context.Context, audio []byte, mimetype string) (string, error)
}

// Config configures the WhatsApp transport. Field docs live on the
// build-tagged constructors (client.go for the whatsmeow-backed
// build, stub_no_whatsmeow.go for the compiled-out variant); the
// struct itself is tag-free so cli/whatsapp.go compiles under both
// builds.
type Config struct {
	// StoreDSN is the modernc.org/sqlite DSN used for the whatsmeow
	// device store. Required in the default build; ignored in the
	// no_whatsmeow stub.
	StoreDSN string
	// LogLevel is the whatsmeow log verbosity (DEBUG/INFO/WARN/ERROR).
	LogLevel string
	// ReplyHeader is prepended to every outbound reply. Empty leaves
	// the message body unmodified.
	ReplyHeader string
	// Transcriber, when non-nil, turns inbound voice notes into text
	// before the router sees them.
	Transcriber Transcriber
}

// DefaultReplyHeader is the string prepended to every outbound reply
// when Config.ReplyHeader is empty.
const DefaultReplyHeader = "💎 *Rousseau Agent*\n\n"
