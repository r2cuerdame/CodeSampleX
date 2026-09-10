package compatibility

import "github.com/r2cuerdame/codesamplex/internal/serverstore"

// The production store must offer every bounded-page contract the builder
// prefers. Each is optional in the type system so that alternate stores can
// keep the row-at-a-time contract, which means a signature drift on the
// PostgreSQL side would still compile -- and the builder would fall back to
// one checkout per row without a word (#174). These assertions make that
// drift a build failure instead.
var (
	_ packageProbeStore            = (*serverstore.PG)(nil)
	_ packageRegisterStore         = (*serverstore.PG)(nil)
	_ receiptPageStore             = (*serverstore.PG)(nil)
	_ jobPageStore                 = (*serverstore.PG)(nil)
	_ targetEvidenceBatchStore     = (*serverstore.PG)(nil)
	_ incrementalSourceStore       = (*serverstore.PG)(nil)
	_ builderRepairGenerationStore = (*serverstore.PG)(nil)
)
