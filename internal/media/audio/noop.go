package audio

import "context"

// Noop is a deterministic stub used in tests. It returns Text
// verbatim and reports Language + Duration if the caller populated
// them; the audio bytes are ignored.
type Noop struct {
	Text     string
	Language string
	Duration int // seconds
}

// Kind returns "noop".
func (*Noop) Kind() string { return "noop" }

// Transcribe returns the pre-configured Result. audio + mimetype
// are ignored (bytes discarded).
func (n *Noop) Transcribe(_ context.Context, _ []byte, _ string) (Result, error) {
	return Result{
		Text:     n.Text,
		Language: n.Language,
	}, nil
}
