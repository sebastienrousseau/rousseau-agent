package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
)

// exportFromSink runs records through a real chained OTLP sink and
// writes every pushed payload to a file, as a collector's file
// exporter would.
func exportFromSink(t *testing.T, key []byte, recs ...audit_egress.Record) string {
	t.Helper()
	var (
		mu   sync.Mutex
		body bytes.Buffer
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		mu.Lock()
		body.Write(b)
		body.WriteByte('\n')
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	inner, err := audit_egress.NewOTLPHTTPSink(audit_egress.Config{
		Kind: audit_egress.KindOTLPHTTP, Endpoint: srv.URL, FlushInterval: 10 * time.Millisecond,
	}, nil)
	require.NoError(t, err)
	sink := audit_egress.NewChainedSink(inner, audit_egress.WithChainHMACKey(key))
	for _, r := range recs {
		require.NoError(t, sink.Emit(context.Background(), r))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, sink.Close(ctx))
	path := filepath.Join(t.TempDir(), "export.jsonl")
	mu.Lock()
	defer mu.Unlock()
	require.NoError(t, os.WriteFile(path, body.Bytes(), 0o600))
	return path
}

func writeTestChainKey(t *testing.T, key []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "chain.key")
	require.NoError(t, os.WriteFile(p, append(append([]byte{}, key...), '\n'), 0o600))
	return p
}

func auditTestRecords() []audit_egress.Record {
	return []audit_egress.Record{
		{Category: "daemon", Actor: "rousseau", Verb: "start", Object: "daemon", Result: "success"},
		{Category: "tool_call", Actor: "alice", Verb: "run", Object: "bash", Result: "success", Detail: map[string]any{"cmd": "ls", "n": 3}},
		{Category: "tool_call", Actor: "alice", Verb: "run", Object: "bash", Result: "denied"},
	}
}

func TestAuditVerify_ExportFromSinkVerifies(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	export := exportFromSink(t, key, auditTestRecords()...)
	var out, errOut bytes.Buffer
	require.NoError(t, auditVerifyRun(&out, &errOut, export, writeTestChainKey(t, key), false))
	assert.Contains(t, out.String(), "OK")
	assert.Contains(t, out.String(), "3 records")
}

func TestAuditVerify_TamperedExportFails(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	export := exportFromSink(t, key, auditTestRecords()...)
	raw, err := os.ReadFile(export)
	require.NoError(t, err)
	raw = bytes.Replace(raw, []byte(`"denied"`), []byte(`"success"`), 1)
	require.NoError(t, os.WriteFile(export, raw, 0o600))
	var out, errOut bytes.Buffer
	err = auditVerifyRun(&out, &errOut, export, "", false)
	require.Error(t, err)
	assert.Contains(t, errOut.String(), "FAILED")
}

func TestAuditVerify_WrongKeyFails(t *testing.T) {
	export := exportFromSink(t, []byte("0123456789abcdef0123456789abcdef"), auditTestRecords()...)
	var out, errOut bytes.Buffer
	err := auditVerifyRun(&out, &errOut, export, writeTestChainKey(t, []byte("ffffffffffffffffffffffffffffffff")), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MAC")
}

func TestAuditVerify_EmptyExportFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.json")
	require.NoError(t, os.WriteFile(p, nil, 0o600))
	var out, errOut bytes.Buffer
	assert.Error(t, auditVerifyRun(&out, &errOut, p, "", false))
}

func TestAuditVerify_MissingFileFails(t *testing.T) {
	var out, errOut bytes.Buffer
	assert.Error(t, auditVerifyRun(&out, &errOut, filepath.Join(t.TempDir(), "nope"), "", false))
}

func TestAuditCmd_Registered(t *testing.T) {
	root := NewRoot(&Options{})
	cmd, _, err := root.Find([]string{"audit", "verify"})
	require.NoError(t, err)
	assert.Equal(t, "verify <file>", cmd.Use)
}
