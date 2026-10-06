package audit_egress

import "github.com/prometheus/client_golang/prometheus"

// Collector exposes the sink's counters as Prometheus metrics. The
// daemon registers it on the shared registry at startup; before this
// the drop counter lived only in Stats(), so an audit stream losing
// records under back-pressure was invisible to alerting, which is
// the one failure mode a DORA or HIPAA buyer asks about first.
func (s *OTLPHTTPSink) Collector() prometheus.Collector {
	return &sinkCollector{s: s}
}

type sinkCollector struct {
	s *OTLPHTTPSink
}

var (
	descEnqueued = prometheus.NewDesc("rousseau_audit_egress_records_enqueued_total",
		"Audit records accepted into the egress queue.", nil, nil)
	descPushed = prometheus.NewDesc("rousseau_audit_egress_records_pushed_total",
		"Audit records successfully delivered to the collector.", nil, nil)
	descDropped = prometheus.NewDesc("rousseau_audit_egress_records_dropped_total",
		"Audit records dropped because the queue or retry buffer was full.", nil, nil)
	descFailed = prometheus.NewDesc("rousseau_audit_egress_push_failures_total",
		"Batch pushes that failed after retries.", nil, nil)
	descPending = prometheus.NewDesc("rousseau_audit_egress_queue_depth",
		"Audit records waiting in the egress queue.", nil, nil)
)

func (c *sinkCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descEnqueued
	ch <- descPushed
	ch <- descDropped
	ch <- descFailed
	ch <- descPending
}

func (c *sinkCollector) Collect(ch chan<- prometheus.Metric) {
	st := c.s.Stats()
	ch <- prometheus.MustNewConstMetric(descEnqueued, prometheus.CounterValue, float64(st.Enqueued))
	ch <- prometheus.MustNewConstMetric(descPushed, prometheus.CounterValue, float64(st.Pushed))
	ch <- prometheus.MustNewConstMetric(descDropped, prometheus.CounterValue, float64(st.Dropped))
	ch <- prometheus.MustNewConstMetric(descFailed, prometheus.CounterValue, float64(st.Failed))
	ch <- prometheus.MustNewConstMetric(descPending, prometheus.GaugeValue, float64(st.Pending))
}
