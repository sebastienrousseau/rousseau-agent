package audio

import "context"

// TranscriberString wraps a Backend so it satisfies the older
// `Transcribe(ctx, audio, mimetype) (string, error)` shape that
// existing transports (WhatsApp Config.Transcriber, and every
// future transport that copies the same interface) expect.
//
// Callers construct it once at daemon start and pass the returned
// value into each transport's Config.Transcriber slot.
//
// Empty transcripts + backend errors both surface a non-nil error to
// the caller — the transport-side dispatchers treat that as "no
// transcript" and skip the audio message rather than replying with
// a synthetic error to the user.
type TranscriberString struct {
	Backend Backend
}

// NewTranscriberString wraps b. Passing a nil Backend returns nil
// (matches "no transcriber configured" semantics — a nil
// Transcriber tells transports to skip audio entirely).
func NewTranscriberString(b Backend) *TranscriberString {
	if b == nil {
		return nil
	}
	return &TranscriberString{Backend: b}
}

// Transcribe adapts the Backend result to the (string, error) shape
// downstream transports expect.
func (t *TranscriberString) Transcribe(ctx context.Context, audio []byte, mimetype string) (string, error) {
	res, err := t.Backend.Transcribe(ctx, audio, mimetype)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}
