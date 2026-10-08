package audit_egress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// OTLP attribute keys the chain verifier reads back.
const (
	attrCategory      = "rousseau.audit.category"
	attrActor         = "rousseau.audit.actor"
	attrVerb          = "rousseau.audit.verb"
	attrObject        = "rousseau.audit.object"
	attrResult        = "rousseau.audit.result"
	attrDetail        = "rousseau.audit.detail"
	attrChainSequence = "rousseau.audit.chain.sequence"
	attrChainHash     = "rousseau.audit.chain.hash"
	attrChainPrevHash = "rousseau.audit.chain.prev_hash"
	attrChainMAC      = "rousseau.audit.chain.mac"
	attrChainVersion  = "rousseau.audit.chain.version"
)

type otlpExport struct {
	ResourceLogs []struct {
		ScopeLogs []struct {
			LogRecords []otlpLogRecord `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type otlpLogRecord struct {
	TimeUnixNano json.RawMessage `json:"timeUnixNano"`
	TraceID      string          `json:"traceId"`
	Attributes   []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	} `json:"attributes"`
}

// ParseOTLPLogs reads OTLP/HTTP JSON log payloads, as the OTLP sink
// pushes them or a collector's file exporter writes them (one or more
// payloads, concatenated or one per line), and returns the chained
// audit records in file order. Records without a chain hash are
// skipped. Pass the result to [Verify].
func ParseOTLPLogs(r io.Reader) ([]Record, error) {
	dec := json.NewDecoder(r)
	var out []Record
	for {
		var p otlpExport
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("otlp export: %w", err)
		}
		recs, err := recordsFromExport(p)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
}

func recordsFromExport(p otlpExport) ([]Record, error) {
	var out []Record
	for _, rl := range p.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				rec, ok, err := recordFromOTLP(lr)
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, rec)
				}
			}
		}
	}
	return out, nil
}

// recordFromOTLP rebuilds one Record; ok is false for an unchained
// log record.
func recordFromOTLP(lr otlpLogRecord) (rec Record, ok bool, err error) {
	attrs := make(map[string]string, len(lr.Attributes))
	for _, a := range lr.Attributes {
		attrs[a.Key] = a.Value.StringValue
	}
	if attrs[attrChainHash] == "" {
		return Record{}, false, nil
	}
	rec = Record{
		Category: attrs[attrCategory], Actor: attrs[attrActor], Verb: attrs[attrVerb],
		Object: attrs[attrObject], Result: attrs[attrResult], TraceID: lr.TraceID,
	}
	rec.Chain = ChainInfo{Hash: attrs[attrChainHash], PrevHash: attrs[attrChainPrevHash], MAC: attrs[attrChainMAC]}
	if rec.At, err = parseUnixNano(lr.TimeUnixNano); err != nil {
		return Record{}, false, err
	}
	if err = parseChainNumbers(attrs, &rec.Chain); err != nil {
		return Record{}, false, err
	}
	if rec.Detail, err = parseDetail(attrs[attrDetail]); err != nil {
		return Record{}, false, err
	}
	return rec, true, nil
}

func parseChainNumbers(attrs map[string]string, c *ChainInfo) error {
	seq, err := strconv.ParseUint(attrs[attrChainSequence], 10, 64)
	if err != nil {
		return fmt.Errorf("otlp export: chain sequence: %w", err)
	}
	c.Sequence = seq
	if v := attrs[attrChainVersion]; v != "" {
		n, err := strconv.ParseUint(v, 10, 8)
		if err != nil {
			return fmt.Errorf("otlp export: chain version: %w", err)
		}
		c.Version = uint8(n)
	}
	return nil
}

// parseUnixNano accepts timeUnixNano as a JSON string or number.
func parseUnixNano(raw json.RawMessage) (time.Time, error) {
	s := strings.Trim(string(raw), `"`)
	if s == "" {
		return time.Time{}, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("otlp export: timeUnixNano: %w", err)
	}
	return time.Unix(0, n).UTC(), nil
}

// parseDetail decodes the exported detail JSON, keeping numbers as
// written so the canonical form re-renders byte for byte.
func parseDetail(s string) (map[string]any, error) {
	if s == "" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var d map[string]any
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("otlp export: detail: %w", err)
	}
	return d, nil
}
