// Package history holds the storage-independent rules of the
// append-only message layout both session stores use: how a message
// is encoded and hashed, what the search index holds for it, and how
// a rewritten conversation lines up with the rows already stored.
package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Encoded is a message as stored: its JSON, the hash of that JSON, and
// the text the search index holds.
type Encoded struct {
	JSON []byte
	Hash string
	Body string
}

// Encode marshals m and hashes the bytes.
func Encode(m agent.Message) (Encoded, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return Encoded{}, fmt.Errorf("marshal message: %w", err)
	}
	sum := sha256.Sum256(b)
	return Encoded{JSON: b, Hash: hex.EncodeToString(sum[:]), Body: Text(m)}, nil
}

// EncodeAll encodes every message of a conversation.
func EncodeAll(msgs []agent.Message) ([]Encoded, error) {
	out := make([]Encoded, len(msgs))
	for i, m := range msgs {
		e, err := Encode(m)
		if err != nil {
			return nil, err
		}
		out[i] = e
	}
	return out, nil
}

// Text is the text of a message's text blocks, one per line; JSON keys
// and image bytes are deliberately left out of the index.
func Text(m agent.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Kind == agent.ContentText && c.Text != "" {
			b.WriteString(c.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ViewHash fingerprints the first n messages of a view: the hash of
// their hashes in order. Stores keep it for the stored view so a save
// can tell an append from a rewrite without reading the rows.
func ViewHash(msgs []Encoded, n int) string {
	h := sha256.New()
	for _, m := range msgs[:n] {
		_, _ = h.Write([]byte(m.Hash)) // hash.Hash.Write never returns an error
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Grew reports whether msgs is the stored view (count messages with
// fingerprint viewHash) plus zero or more new messages: the common
// case, where a save only appends msgs[count:]. Every stored message
// is compared, so a rewrite that keeps the length or the last message
// is still a rewrite.
func Grew(msgs []Encoded, count int, viewHash string) bool {
	return count <= len(msgs) && ViewHash(msgs, count) == viewHash
}

// Alignment says how a rewritten view maps onto the stored rows.
type Alignment struct {
	// Head is how many leading messages are not rows (a summary).
	Head int
	// Base is the index into the stored rows of the first row the
	// view still contains; len(stored) when it contains none.
	Base int
	// AppendFrom is the index of the first message to append.
	AppendFrom int
}

// Align finds the longest run of stored rows, ending at the last
// stored row, inside msgs. Messages before the run become head and
// messages after it are appended. With no such run, the whole view is
// appended and no stored row stays in the view. stored holds the row
// hashes in seq order.
func Align(stored []string, msgs []Encoded) Alignment {
	n := len(stored)
	bestLen, bestEnd := 0, -1
	if n > 0 {
		for p := range msgs {
			if msgs[p].Hash != stored[n-1] {
				continue
			}
			run := 0
			for run <= p && run < n && msgs[p-run].Hash == stored[n-1-run] {
				run++
			}
			if run > bestLen {
				bestLen, bestEnd = run, p
			}
		}
	}
	if bestLen == 0 {
		return Alignment{Head: 0, Base: n, AppendFrom: 0}
	}
	start := bestEnd - bestLen + 1
	return Alignment{Head: start, Base: n - bestLen, AppendFrom: bestEnd + 1}
}

// HeadJSON is the JSON array of msgs[:a.Head], "[]" when empty.
func HeadJSON(msgs []Encoded, a Alignment) string {
	if a.Head == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, m := range msgs[:a.Head] {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(m.JSON)
	}
	b.WriteByte(']')
	return b.String()
}
