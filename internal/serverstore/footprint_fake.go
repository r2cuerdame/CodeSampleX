package serverstore

import (
	"context"
	"sort"
	"time"
)

var _ ExecutionFootprintStore = (*Fake)(nil)

func (f *Fake) RecordExecutionFootprint(_ context.Context, row ExecutionFootprintRow, now time.Time) (ExecutionFootprintRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.footprints == nil {
		f.footprints = map[string]*ExecutionFootprintRow{}
	}
	if existing, ok := f.footprints[row.DedupKey]; ok {
		existing.Outcome = row.Outcome
		existing.FailureFingerprint = row.FailureFingerprint
		existing.Environment = row.Environment
		existing.UpdatedAt = now
		return *existing, true, nil
	}
	f.nextFootprintID++
	row.ID = f.nextFootprintID
	row.CreatedAt = now
	row.UpdatedAt = now
	stored := row
	f.footprints[row.DedupKey] = &stored
	return stored, false, nil
}

func (f *Fake) ExecutionFootprintCounts(_ context.Context, sampleID string) (ExecutionFootprintCounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out ExecutionFootprintCounts
	for _, row := range f.footprints {
		if row.SampleID != sampleID {
			continue
		}
		switch row.Outcome {
		case "pass":
			out.Pass++
		case "fail":
			out.Fail++
		case "could_not_run":
			out.CouldNotRun++
		}
	}
	return out, nil
}

func (f *Fake) ListExecutionFootprints(_ context.Context, limit int) ([]ExecutionFootprintRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ExecutionFootprintRow
	for _, row := range f.footprints {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
