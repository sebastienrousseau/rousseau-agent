// Package whatsapp — types defined here are shared across the
// standard (whatsmeow-backed) and no_whatsmeow (stub) builds, so
// this file carries no build tag and no whatsmeow imports.
package whatsapp

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

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
	// Progress, when non-nil, is the bus the transport subscribes its
	// live-update reporter to. The daemon shares one bus between this
	// Config (sink side) and agent.Options (publisher side) so per-tool
	// progress events flow end-to-end. Empty falls back to a per-Client
	// internal bus — useful for tests and embedded use where nothing
	// on the publisher side exists.
	Progress *progress.Bus
	// Allowlist restricts which sender JIDs the transport reacts to
	// with visible receipt / completion markers. A message from any
	// JID NOT on this list is silently dropped inside Dispatch — no
	// emoji reactions, no typing indicator, no placeholder message.
	// The daemon-scoped Router still applies its own allowlist as a
	// second gate; this list exists so the WhatsApp UX never leaks
	// the fact that the number is bot-monitored to strangers.
	//
	// Empty means "no restriction" (the transport reacts to everyone
	// it hears from — sensible for unit tests, dangerous in prod).
	Allowlist []string
}

// DefaultReplyHeader is the string prepended to every outbound reply
// when Config.ReplyHeader is empty.
const DefaultReplyHeader = "💎 *Rousseau Agent*\n\n"
