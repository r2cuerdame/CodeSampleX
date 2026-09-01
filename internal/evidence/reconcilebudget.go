package evidence

import (
	"context"
	"fmt"
	"time"
)

// reconcileBudget bounds the legacy-Windows reconciliation pass.
//
// The pass reads every observation whose exit code is in the legacy unsigned
// Windows range and, for each, may issue a point query for the canonical
// row's count. Those rows are never deleted -- reconciling only raises a
// high-water column -- so the scan is over the same set on every sync, and on
// a burst-exhausted 0.4-core node with twenty thousand batches behind it that
// is minutes of CPU before a single byte is uploaded.
//
// Thirty seconds is enough to make progress on a normal machine and short
// enough that the upload still happens on a slow one. Progress is durable:
// whatever the pass reconciled before the budget expired stays reconciled,
// and the next sync resumes from there.
const reconcileBudget = 30 * time.Second

// runReconcile runs the reconciliation under its own budget, independent of
// the caller's. It returns the pass's error rather than acting on it.
func (b *Batcher) runReconcile(ctx context.Context) error {
	run := b.reconcile
	if run == nil {
		run = b.prepareLegacyWindowsReconciliation
	}
	rctx, cancel := context.WithTimeout(ctx, reconcileBudget)
	defer cancel()
	return run(rctx)
}

// noteReconcile records why a pass did not finish, so a queue that is not
// draining can say what is holding it.
//
// The node that reported this could not tell a stalled pass from a hung
// process: 601 seconds with no output at all. A stalled correction is not an
// error the caller should fail on, but it is a fact somebody has to be able
// to read.
func (b *Batcher) noteReconcile(err error) {
	b.reconcileMu.Lock()
	defer b.reconcileMu.Unlock()
	if err == nil {
		b.reconcileNote = ""
		return
	}
	b.reconcileNote = fmt.Sprintf(
		"legacy Windows exit-code reconciliation did not finish within %s (%v); "+
			"uploads continued and the pass resumes on the next sync", reconcileBudget, err)
}

// LastReconcileNote reports the most recent unfinished reconciliation, or ""
// when the last pass completed.
func (b *Batcher) LastReconcileNote() string {
	b.reconcileMu.Lock()
	defer b.reconcileMu.Unlock()
	return b.reconcileNote
}
