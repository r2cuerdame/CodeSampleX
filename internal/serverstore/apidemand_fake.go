package serverstore

import (
	"context"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/apidemand"
)

var _ apidemand.Store = (*Fake)(nil)

// UpsertDemand implements apidemand.Store over the in-memory store so the
// server wiring records demand against the fake exactly as it does against
// PostgreSQL.
func (f *Fake) UpsertDemand(ctx context.Context, batch apidemand.Batch) error {
	return f.demand.UpsertDemand(ctx, batch)
}

// PruneDemand implements apidemand.Store.
func (f *Fake) PruneDemand(ctx context.Context, before time.Time) error {
	return f.demand.PruneDemand(ctx, before)
}

// DemandReport implements apidemand.Store.
func (f *Fake) DemandReport(ctx context.Context, now time.Time) (apidemand.Report, error) {
	return f.demand.DemandReport(ctx, now)
}
