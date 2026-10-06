package audit_egress

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The collector must expose every Stats counter so queue drops are
// alertable rather than visible only through doctor.
func TestOTLPHTTPSink_CollectorExposesStats(t *testing.T) {
	s, err := NewOTLPHTTPSink(Config{
		Kind:          KindOTLPHTTP,
		Endpoint:      "http://127.0.0.1:9/v1/logs",
		QueueSize:     1,
		FlushInterval: time.Hour, // the pusher never drains during the test
	}, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(ctx) //nolint:errcheck // the drain to a closed port is expected to fail
	})

	// Fill the queue of one and overflow it so Dropped moves.
	for i := 0; i < 3; i++ {
		require.NoError(t, s.Emit(context.Background(), Record{Category: "test", Actor: "a", Verb: "v", Object: "o", Result: "success"}))
	}

	c := s.Collector()
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(c))
	assert.Equal(t, 5, testutil.CollectAndCount(c))

	st := s.Stats()
	assert.Equal(t, int64(3), st.Enqueued)
	assert.GreaterOrEqual(t, st.Dropped, int64(1), "a queue of one overflowed")
	assert.Equal(t, 1, st.Pending)

	expected := strings.NewReader(`
# HELP rousseau_audit_egress_records_enqueued_total Audit records accepted into the egress queue.
# TYPE rousseau_audit_egress_records_enqueued_total counter
rousseau_audit_egress_records_enqueued_total ` + strconv.FormatInt(st.Enqueued, 10) + `
# HELP rousseau_audit_egress_records_dropped_total Audit records dropped because the queue or retry buffer was full.
# TYPE rousseau_audit_egress_records_dropped_total counter
rousseau_audit_egress_records_dropped_total ` + strconv.FormatInt(st.Dropped, 10) + `
# HELP rousseau_audit_egress_queue_depth Audit records waiting in the egress queue.
# TYPE rousseau_audit_egress_queue_depth gauge
rousseau_audit_egress_queue_depth 1
`)
	assert.NoError(t, testutil.GatherAndCompare(reg, expected,
		"rousseau_audit_egress_records_enqueued_total",
		"rousseau_audit_egress_records_dropped_total",
		"rousseau_audit_egress_queue_depth"))
}
