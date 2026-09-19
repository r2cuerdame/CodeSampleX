package serverstore

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
)

var _ FixClaimStore = (*Fake)(nil)

type fakeFixState struct {
	rows     map[int64]*FixCandidateRow
	dedup    map[string]int64
	runs     map[int64][]FixRunRow
	nextID   int64
	nextRun  int64
	ingested int64
	rejected int64
}

func (f *Fake) fix() *fakeFixState {
	if f.fixState == nil {
		f.fixState = &fakeFixState{rows: map[int64]*FixCandidateRow{}, dedup: map[string]int64{}, runs: map[int64][]FixRunRow{}}
	}
	return f.fixState
}

func (f *Fake) UpsertFixCandidates(_ context.Context, rows []FixCandidateRow, rejected int64, now time.Time) ([]FixCandidateUpsert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fix()
	st.ingested += int64(len(rows)) + rejected
	st.rejected += rejected
	out := make([]FixCandidateUpsert, 0, len(rows))
	for _, row := range rows {
		key := row.Candidate.DedupKey()
		if id, ok := st.dedup[key]; ok {
			out = append(out, FixCandidateUpsert{Row: *st.rows[id], Duplicate: true})
			continue
		}
		st.nextID++
		row.ID = st.nextID
		row.DedupKey = key
		row.Candidate = row.Candidate.Normalized()
		row.Status = fixclaims.StatusClaimedFix
		row.PairOutcome = fixclaims.PairPending
		row.Evaluation = fixclaims.Evaluation{Status: fixclaims.StatusClaimedFix}
		row.CreatedAt, row.UpdatedAt = now, now
		stored := row
		st.rows[row.ID] = &stored
		st.dedup[key] = row.ID
		out = append(out, FixCandidateUpsert{Row: stored})
	}
	return out, nil
}

func (f *Fake) ClaimFixWork(_ context.Context, sessionID string, limits FixClaimLimits, now, leaseExpiresAt time.Time) (FixCandidateRow, FixClaimStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fix()
	if limits.MaxLeases <= 0 {
		limits.MaxLeases = DefaultFixClaimLimits().MaxLeases
	}
	live := 0
	var held *FixCandidateRow
	for _, row := range st.rows {
		if !row.Leased(now) {
			continue
		}
		live++
		if row.ClaimedBy == sessionID {
			held = row
		}
	}
	if held != nil {
		held.LeaseExpiresAt = leaseExpiresAt
		return *held, FixClaimAssigned, nil
	}
	if live >= limits.MaxLeases {
		return FixCandidateRow{}, FixClaimBudgetExhausted, nil
	}
	var open []*FixCandidateRow
	for _, row := range st.rows {
		if row.Closed || row.Leased(now) {
			continue
		}
		open = append(open, row)
	}
	if len(open) == 0 {
		return FixCandidateRow{}, FixClaimNoWork, nil
	}
	sort.Slice(open, func(i, j int) bool {
		if open[i].Attempts != open[j].Attempts {
			return open[i].Attempts < open[j].Attempts
		}
		if open[i].Score != open[j].Score {
			return open[i].Score > open[j].Score
		}
		return open[i].ID < open[j].ID
	})
	row := open[0]
	row.ClaimedBy = sessionID
	row.ClaimedAt = now
	row.LeaseExpiresAt = leaseExpiresAt
	row.Attempts++
	row.UpdatedAt = now
	return *row, FixClaimAssigned, nil
}

func (f *Fake) leasedFixRow(id int64, sessionID string, now time.Time) (*FixCandidateRow, error) {
	st := f.fix()
	row, ok := st.rows[id]
	if !ok {
		return nil, ErrFixCandidateMissing
	}
	if !row.Leased(now) || row.ClaimedBy != sessionID {
		return nil, ErrFixLeaseMissing
	}
	return row, nil
}

func (f *Fake) SetFixReproducer(_ context.Context, id int64, sessionID string, source fixclaims.ReproducerSource, sampleID string, now time.Time) (FixCandidateRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, err := f.leasedFixRow(id, sessionID, now)
	if err != nil {
		return FixCandidateRow{}, err
	}
	row.ReproducerSource = source
	if sampleID != "" {
		row.ReproducerSampleID = sampleID
	}
	row.UpdatedAt = now
	return *row, nil
}

func (f *Fake) RecordFixRuns(_ context.Context, id int64, sessionID string, runs []fixclaims.Run, limits FixClaimLimits, now time.Time) (FixCandidateRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, err := f.leasedFixRow(id, sessionID, now)
	if err != nil {
		return FixCandidateRow{}, err
	}
	st := f.fix()
	for _, r := range runs {
		st.nextRun++
		if r.ObservedAt.IsZero() {
			r.ObservedAt = now
		}
		st.runs[id] = append(st.runs[id], FixRunRow{ID: st.nextRun, CandidateID: id, SessionID: sessionID, Run: r, CreatedAt: now})
	}
	all := make([]fixclaims.Run, 0, len(st.runs[id]))
	for _, r := range st.runs[id] {
		all = append(all, r.Run)
	}
	applyFixRuns(row, all, now)
	row.ClaimedBy, row.ClaimedAt, row.LeaseExpiresAt = "", time.Time{}, time.Time{}
	settleFixCandidate(row, limits)
	return *row, nil
}

func (f *Fake) ReleaseFixWork(_ context.Context, id int64, sessionID string, outcome FixWorkOutcome, detail string, limits FixClaimLimits, now time.Time) (FixCandidateRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, err := f.leasedFixRow(id, sessionID, now)
	if err != nil {
		return FixCandidateRow{}, err
	}
	row.ClaimedBy, row.ClaimedAt, row.LeaseExpiresAt = "", time.Time{}, time.Time{}
	row.UpdatedAt = now
	switch outcome {
	case FixOutcomeComplete:
		row.Closed, row.ClosedReason = true, FixClosedComplete
	case FixOutcomeInfrastructure, FixOutcomeTransient:
		// The writer's own failure is not evidence about the candidate:
		// the attempt is refunded, once per handout.
		if row.Attempts > 0 {
			row.Attempts--
		}
	}
	settleFixCandidate(row, limits)
	return *row, nil
}

func (f *Fake) GetFixCandidate(_ context.Context, id int64) (FixCandidateRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.fix().rows[id]
	if !ok {
		return FixCandidateRow{}, false, nil
	}
	return *row, true, nil
}

func (f *Fake) ListFixRuns(_ context.Context, id int64) ([]FixRunRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FixRunRow(nil), f.fix().runs[id]...), nil
}

func (f *Fake) ListFixCandidates(_ context.Context, q FixClaimQuery, limit int) ([]FixCandidateRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	var out []FixCandidateRow
	for _, row := range f.fix().rows {
		if !fixQueryMatches(*row, q) {
			continue
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func fixQueryMatches(row FixCandidateRow, q FixClaimQuery) bool {
	c := row.Candidate
	if q.Ecosystem != "" && c.Ecosystem != strings.ToLower(q.Ecosystem) {
		return false
	}
	if q.Name != "" && !strings.EqualFold(c.Name, q.Name) {
		return false
	}
	if q.Version != "" && c.ClaimedFixedVersion != q.Version && c.ClaimedBadVersion != q.Version {
		return false
	}
	if q.Status != "" && row.Status != q.Status {
		return false
	}
	if q.Open && row.Closed {
		return false
	}
	return true
}

func (f *Fake) FixClaimStates(_ context.Context) ([]fixclaims.CandidateState, int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.fix()
	states := make([]fixclaims.CandidateState, 0, len(st.rows))
	for _, row := range st.rows {
		states = append(states, row.State())
	}
	return states, st.ingested, st.rejected, nil
}
