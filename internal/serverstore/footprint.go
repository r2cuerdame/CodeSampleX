package serverstore

import (
	"context"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ExecutionFootprintRow is one stored zero-install execution footprint
// (#318): a self-reported, unsigned "I ran this sample and this is what
// happened" from a caller that reached the network over plain HTTPS.
//
// It is kept apart from adoptions and from evidence_agg on purpose. No
// sanitizer ran on the caller's machine, no receipt vouches for the
// environment, and no local offer proves the sample was the thing that
// ran. It is a usage trace, and it is filed under EXECUTION_FOOTPRINT,
// which weighs zero in compatibility.ClassWeight.
type ExecutionFootprintRow struct {
	ID int64
	// DedupKey is the identity of the event: one source, one sample, one
	// stage, one UTC day. The same caller retrying all afternoon is one
	// row whose outcome is the latest it reported.
	DedupKey           string
	SampleID           string
	Outcome            domain.FootprintOutcome
	Stage              domain.FootprintStage
	Environment        domain.FootprintEnvironment
	FailureFingerprint string
	// Epoch is the UTC day, and SourceBucket is SHA-256(epoch | client
	// address). The address itself is never stored, and the bucket is
	// useless after the day it was made for.
	Epoch        string
	SourceBucket string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ExecutionFootprintCounts is the bounded per-sample view: how many
// distinct daily sources reported each outcome. It is what a sample page or
// an operator may show, labelled as unsigned self-reports; it is never a
// grade input.
type ExecutionFootprintCounts struct {
	Pass        int64 `json:"pass"`
	Fail        int64 `json:"fail"`
	CouldNotRun int64 `json:"couldNotRun"`
}

// Total is the number of footprints across every outcome.
func (c ExecutionFootprintCounts) Total() int64 { return c.Pass + c.Fail + c.CouldNotRun }

// ExecutionFootprintStore is the zero-install half of the adoption loop.
//
// It is an optional capability: a store that does not implement it makes
// the endpoint answer 503, never an accepted write that went nowhere.
type ExecutionFootprintStore interface {
	// RecordExecutionFootprint stores a footprint or updates the one the
	// same source already filed for the same sample, stage and day.
	// duplicate=true means the row existed and its outcome was replaced.
	RecordExecutionFootprint(ctx context.Context, row ExecutionFootprintRow, now time.Time) (stored ExecutionFootprintRow, duplicate bool, err error)
	// ExecutionFootprintCounts tallies one sample's footprints by outcome.
	ExecutionFootprintCounts(ctx context.Context, sampleID string) (ExecutionFootprintCounts, error)
	// ListExecutionFootprints returns the newest footprints, bounded.
	ListExecutionFootprints(ctx context.Context, limit int) ([]ExecutionFootprintRow, error)
}
