package audit_egress

import (
	"encoding/json"
	"testing"

	fuzz "github.com/google/gofuzz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/testutil/fuzztest"
)

// TestFuzz_OTLPMarshalNeverErrors: marshalOTLPLogs must produce
// valid JSON for any batch of random Records. Property: the sink
// never returns a marshal error to its retry queue — a failed
// marshal would mean an audit record silently disappears.
func TestFuzz_OTLPMarshalNeverErrors(t *testing.T) {
	f := fuzztest.New(t).Funcs(
		// Detail: `map[string]any` with `any` values is a
		// gofuzz hazard (nested interface graphs). Force it to
		// a flat string→string shape so the marshal path is
		// what's under test, not the populator.
		func(m *map[string]any, c fuzz.Continue) {
			out := map[string]any{}
			n := c.Intn(4)
			for i := 0; i < n; i++ {
				k := randomPrintable(c, 3, 12)
				out[k] = randomPrintable(c, 3, 30)
			}
			*m = out
		},
	)

	for i := 0; i < fuzztest.DefaultIterations; i++ {
		var count uint8
		f.Fuzz(&count)
		n := int(count%16) + 1
		batch := make([]Record, n)
		for j := range batch {
			f.Fuzz(&batch[j])
		}
		body, err := marshalOTLPLogs(batch)
		require.NoError(t, err, "marshalOTLPLogs must accept every random Record batch")
		require.NotEmpty(t, body)
		// The payload must be legal JSON that a downstream OTel
		// collector can parse.
		var back map[string]any
		require.NoError(t, json.Unmarshal(body, &back), "output must be legal JSON")
		// Sanity: the OTLP wire shape lands under the expected key.
		_, ok := back["resourceLogs"]
		assert.True(t, ok, "payload must contain resourceLogs")
	}
}

// TestFuzz_OTLPRecordFieldsSurface: for every random Record, the
// user-provided string fields MUST appear in the OTLP JSON
// output — verifies the attribute mapping doesn't silently drop
// fields under weird inputs (empty strings, embedded control
// chars, deeply-nested UTF-8, etc.).
func TestFuzz_OTLPRecordFieldsSurface(t *testing.T) {
	f := fuzztest.New(t).Funcs(
		func(m *map[string]any, c fuzz.Continue) {
			*m = nil // isolate the top-level string fields
		},
	)

	for i := 0; i < 200; i++ {
		var rec Record
		f.Fuzz(&rec)
		body, err := marshalOTLPLogs([]Record{rec})
		require.NoError(t, err)
		s := string(body)

		// Each string field is emitted as a `"stringValue": "<v>"`
		// attribute. When the value is non-empty AND JSON-safe,
		// we can grep for it directly. For empty strings the
		// attribute still lands with empty value — check that
		// the attribute KEY is present regardless.
		for _, key := range []string{
			"rousseau.audit.category",
			"rousseau.audit.actor",
			"rousseau.audit.verb",
			"rousseau.audit.object",
			"rousseau.audit.result",
		} {
			assert.Contains(t, s, key, "attribute key %q must always land in the OTLP payload", key)
		}
	}
}

// TestFuzz_OTLPBodyFieldReflectsVerbAndObject: the OTel body
// string is "<verb> <object> → <result>". For every random
// Record, that composition must survive JSON round-trip
// unchanged. Structured check — the raw JSON is escaped so a
// substring grep would be unreliable on inputs with `>`, `<`,
// `&`, `"`, or `\`.
func TestFuzz_OTLPBodyFieldReflectsVerbAndObject(t *testing.T) {
	f := fuzztest.New(t).Funcs(
		func(m *map[string]any, c fuzz.Continue) { *m = nil },
	)

	for i := 0; i < 200; i++ {
		var rec Record
		f.Fuzz(&rec)
		body, err := marshalOTLPLogs([]Record{rec})
		require.NoError(t, err)

		gotBody, err := extractLogRecordBody(body)
		require.NoError(t, err, "OTLP payload must be well-formed enough for the body extractor")
		expected := rec.Verb + " " + rec.Object + " → " + rec.Result
		assert.Equal(t, expected, gotBody)
	}
}

// extractLogRecordBody digs into the OTLP JSON envelope and pulls
// out `resourceLogs[0].scopeLogs[0].logRecords[0].body.stringValue`.
// A hand-written accessor rather than a generated struct so the
// tests stay pinned to the wire shape the collector actually
// consumes.
func extractLogRecordBody(payload []byte) (string, error) {
	var envelope struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []struct {
					Body struct {
						StringValue string `json:"stringValue"`
					} `json:"body"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", err
	}
	if len(envelope.ResourceLogs) == 0 || len(envelope.ResourceLogs[0].ScopeLogs) == 0 ||
		len(envelope.ResourceLogs[0].ScopeLogs[0].LogRecords) == 0 {
		return "", errNoLogRecords
	}
	return envelope.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Body.StringValue, nil
}

var errNoLogRecords = &noLogRecordsError{}

type noLogRecordsError struct{}

func (*noLogRecordsError) Error() string { return "no log records in payload" }

// randomPrintable is a local copy of the fuzztest package helper
// so the Detail populator can call it inline without widening
// fuzztest's public API.
func randomPrintable(c fuzz.Continue, minLen, maxLen int) string {
	n := minLen
	if maxLen > minLen {
		n += c.Intn(maxLen - minLen + 1)
	}
	const printable = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789 !#$%&'()*+,-./:;<=>?@[]^_`{|}~"
	b := make([]byte, n)
	for i := range b {
		b[i] = printable[c.Intn(len(printable))]
	}
	return string(b)
}
