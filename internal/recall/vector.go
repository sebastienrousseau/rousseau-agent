package recall

import (
	"encoding/binary"
	"errors"
	"math"
)

// EncodeVector serialises v as little-endian float32 bytes suitable
// for a SQLite BLOB column. Compact (dims × 4 bytes), portable across
// architectures.
func EncodeVector(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(f))
	}
	return buf
}

// DecodeVector reverses EncodeVector. Returns an error when the byte
// length isn't a multiple of 4 or doesn't match the expected dims.
func DecodeVector(b []byte, dims int) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, errors.New("recall: blob length not multiple of 4")
	}
	got := len(b) / 4
	if dims > 0 && got != dims {
		return nil, ErrDimensionMismatch
	}
	out := make([]float32, got)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out, nil
}

// CosineSimilarity returns the cosine similarity between a and b.
// Returns 0 when either operand is zero-length or all-zeroes.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		af := float64(a[i])
		bf := float64(b[i])
		dot += af * bf
		na += af * af
		nb += bf * bf
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// Chunk splits text into token-approximate chunks of chunkTokens
// tokens with chunkOverlap tokens of overlap. Token count is
// estimated by whitespace-split word count — cheap, correct within a
// factor of two for English + code, and doesn't drag in a tokeniser
// dependency.
//
// Empty input returns a single empty-string chunk so ingesters don't
// need a special case.
func Chunk(text string, chunkTokens, chunkOverlap int) []string {
	if chunkTokens <= 0 {
		chunkTokens = 400
	}
	if chunkOverlap < 0 {
		chunkOverlap = 40
	}
	if chunkOverlap >= chunkTokens {
		// Clamp to half of chunkTokens so step stays positive.
		chunkOverlap = chunkTokens / 2
	}
	words := splitWords(text)
	if len(words) == 0 {
		return []string{""}
	}
	if len(words) <= chunkTokens {
		return []string{joinWords(words)}
	}
	step := chunkTokens - chunkOverlap
	var out []string
	for start := 0; start < len(words); start += step {
		end := start + chunkTokens
		if end > len(words) {
			end = len(words)
		}
		out = append(out, joinWords(words[start:end]))
		if end == len(words) {
			break
		}
	}
	return out
}

func splitWords(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

func joinWords(words []string) string {
	total := 0
	for _, w := range words {
		total += len(w) + 1
	}
	if total > 0 {
		total--
	}
	buf := make([]byte, 0, total)
	for i, w := range words {
		if i > 0 {
			buf = append(buf, ' ')
		}
		buf = append(buf, w...)
	}
	return string(buf)
}
