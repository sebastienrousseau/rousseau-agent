package sqlite

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/pricing"
)

// costWriter is the minimum SessionCostStore surface CostRecorder
// needs — one method. Both sqlite.SessionCostStore and
// postgres.SessionCostStore satisfy it (they were designed with
// matching signatures during the §2.4a port and CostRecord is
// type-aliased across drivers), so operators can wire either
// backend without a driver-specific recorder shim.
type costWriter interface {
	Record(ctx context.Context, r CostRecord) error
}

// CostRecorder adapts a [SessionCostStore] to the
// [agent.CostRecorder] contract: computes cost from usage via
// [pricing.Estimate] and appends a row to session_costs. When the
// model isn't priced in [pricing.DefaultTable], we still record the
// row with cost_usd = 0 — losing an approximate cost is better than
// dropping the completion record entirely, and operators can compute
// missing costs later once they've filled in the price table.
type CostRecorder struct {
	Store costWriter
	Table pricing.Table // nil uses pricing.DefaultTable
}

// NewCostRecorder wraps a SessionCostStore in the CostRecorder
// adapter. Table may be nil to use the baked-in default price
// sheet. Store accepts anything satisfying costWriter — both
// sqlite.SessionCostStore and postgres.SessionCostStore qualify.
func NewCostRecorder(store costWriter, table pricing.Table) *CostRecorder {
	return &CostRecorder{Store: store, Table: table}
}

// Record satisfies [agent.CostRecorder].
func (r *CostRecorder) Record(ctx context.Context, evt agent.CostEvent) error {
	cost, _ := pricing.Estimate(evt.Usage, evt.Model, r.Table)
	return r.Store.Record(ctx, CostRecord{
		SessionID: evt.SessionID,
		Provider:  evt.Provider,
		Model:     evt.Model,
		Usage:     evt.Usage,
		CostUSD:   cost,
	})
}
