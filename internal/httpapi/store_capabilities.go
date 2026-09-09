package httpapi

import "github.com/r2cuerdame/codesamplex/internal/serverstore"

// The production store must offer every bounded-page contract the handlers
// prefer. Each is optional in the type system so that alternate stores can
// keep the row-at-a-time contract, which means a signature drift on the
// PostgreSQL side would still compile -- and the handler would fall back to
// one checkout per row without a word (#174). These assertions make that
// drift a build failure instead.
var (
	_ packageRowsStore   = (*serverstore.PG)(nil)
	_ snapshotPagesStore = (*serverstore.PG)(nil)
	_ receiptPagesStore  = (*serverstore.PG)(nil)
)
