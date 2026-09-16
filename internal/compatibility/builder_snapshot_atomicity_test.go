package compatibility

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// snapshotSplitDetectingFake wraps serverstore.Fake, feeding RunOnce a fixed
// target list (via ListSnapshotTargets) and recording, for every PutSnapshots
// call, the distinct purls present in that call's rows -- so a purl split
// across two transactions shows up as appearing in more than one call.
type snapshotSplitDetectingFake struct {
	*serverstore.Fake
	targets []serverstore.SnapshotTarget
	calls   [][]string
}

// newSnapshotSplitDetectingFake seeds one purl with more symbol targets than
// fit in one batch, so today's unconditional batch-size flush is forced to
// cut it in half.
func newSnapshotSplitDetectingFake(t *testing.T, batchSize int) *snapshotSplitDetectingFake {
	t.Helper()
	const purl = "pkg:npm/many-symbols@1.0.0"
	n := batchSize + 5
	targets := make([]serverstore.SnapshotTarget, 0, n)
	for i := 0; i < n; i++ {
		targets = append(targets, serverstore.SnapshotTarget{PURL: purl, Symbol: fmt.Sprintf("sym%04d", i)})
	}
	return &snapshotSplitDetectingFake{Fake: serverstore.NewFake(), targets: targets}
}

func (s *snapshotSplitDetectingFake) ListSnapshotTargets(context.Context) ([]serverstore.SnapshotTarget, error) {
	return append([]serverstore.SnapshotTarget(nil), s.targets...), nil
}

func (s *snapshotSplitDetectingFake) PutSnapshots(ctx context.Context, rows []serverstore.SnapshotRow) error {
	seen := map[string]bool{}
	var purls []string
	for _, row := range rows {
		if !seen[row.PURL] {
			seen[row.PURL] = true
			purls = append(purls, row.PURL)
		}
	}
	s.calls = append(s.calls, purls)
	for _, row := range rows {
		if err := s.Fake.PutSnapshot(ctx, row.PURL, row.Symbol, row.SnapshotJSON); err != nil {
			return err
		}
	}
	return nil
}

// splitPurls returns every purl that appeared in more than one PutSnapshots
// call -- a reader querying GetSnapshotsForPURL between those two commits
// would see a mix of old and new symbols for it.
func (s *snapshotSplitDetectingFake) splitPurls() []string {
	count := map[string]int{}
	for _, purls := range s.calls {
		for _, p := range purls {
			count[p]++
		}
	}
	var violations []string
	for p, c := range count {
		if c > 1 {
			violations = append(violations, p)
		}
	}
	return violations
}

// A purl with more symbol targets than fit in one snapshotWriteBatch must
// never be visible to a reader with only some of its symbols updated. This
// test drives RunOnce against a Fake instrumented to record, for every
// PutSnapshots call, whether all rows sharing a purl arrived in the same
// call.
func TestSnapshotFlushNeverSplitsAPurlAcrossTransactions(t *testing.T) {
	fake := newSnapshotSplitDetectingFake(t, snapshotWriteBatch)
	fake.NowFn = func() time.Time { return testNow }
	b := &Builder{Store: fake, Now: func() time.Time { return testNow }}

	ctx := context.Background()
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if violations := fake.splitPurls(); len(violations) > 0 {
		t.Fatalf("purls split across PutSnapshots calls: %v", violations)
	}
}
