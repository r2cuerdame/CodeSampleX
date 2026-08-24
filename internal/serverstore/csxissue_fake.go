package serverstore

import (
	"context"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var _ CSXIssueStore = (*Fake)(nil)

func (f *Fake) RecordCSXIssueReport(_ context.Context, row CSXIssueReportRow, now time.Time) (CSXIssueReportRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.csxIssues == nil {
		f.csxIssues = map[string]*CSXIssueReportRow{}
	}
	if existing, ok := f.csxIssues[row.Fingerprint]; ok {
		existing.Occurrences++
		existing.LastSeen = now
		return *existing, true, nil
	}
	f.nextCSXIssueID++
	row.ID = f.nextCSXIssueID
	row.Occurrences = 1
	row.FirstSeen = now
	row.LastSeen = now
	if row.Status == "" {
		row.Status = domain.CSXIssueStatusTriage
	}
	stored := row
	f.csxIssues[row.Fingerprint] = &stored
	return stored, false, nil
}

func (f *Fake) findCSXIssueLocked(id int64) *CSXIssueReportRow {
	for _, row := range f.csxIssues {
		if row.ID == id {
			return row
		}
	}
	return nil
}

func (f *Fake) CSXIssueReportByID(_ context.Context, id int64) (CSXIssueReportRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.findCSXIssueLocked(id)
	if row == nil {
		return CSXIssueReportRow{}, false, nil
	}
	return *row, true, nil
}

func (f *Fake) SetCSXIssueTriage(_ context.Context, id int64, status, replayReason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row := f.findCSXIssueLocked(id); row != nil {
		row.Status = status
		row.ReplayReason = replayReason
	}
	return nil
}

func (f *Fake) SetCSXIssueVerdict(_ context.Context, id int64, verdict string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.findCSXIssueLocked(id)
	if row == nil || row.Verdict != "" {
		return false, nil
	}
	row.Verdict = verdict
	row.Status = domain.CSXIssueStatusResolved
	row.VerdictAt = at
	return true, nil
}

func (f *Fake) LinkCSXIssueCanonical(_ context.Context, id int64, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.findCSXIssueLocked(id)
	// Only a confirmed defect may be linked. Linking an unconfirmed report
	// to a bug is how a candidate quietly becomes a claim.
	if row == nil || !domain.CSXIssueVerdictConfirmed(row.Verdict) {
		return false, nil
	}
	row.CanonicalRef = ref
	return true, nil
}

func (f *Fake) ListCSXIssueReports(_ context.Context, limit int) ([]CSXIssueReportRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []CSXIssueReportRow
	for _, row := range f.csxIssues {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *Fake) CSXIssueInsights(_ context.Context, now time.Time, days int) (CSXIssueInsights, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start := now.AddDate(0, 0, -days)
	out := CSXIssueInsights{WindowStart: start, WindowEnd: now}
	for _, row := range f.csxIssues {
		if row.LastSeen.Before(start) {
			continue
		}
		out.Occurrences += row.Occurrences
		out.Unique++
		switch row.Status {
		case domain.CSXIssueStatusTriage:
			out.Triage++
		case domain.CSXIssueStatusReplayQueued:
			out.ReplayQueued++
		case domain.CSXIssueStatusNoReplayLane:
			out.NoReplayLane++
		case domain.CSXIssueStatusResolved:
			out.Resolved++
		}
		if domain.CSXIssueVerdictConfirmed(row.Verdict) {
			out.Confirmed++
		}
		if row.CanonicalRef != "" {
			out.Linked++
		}
	}
	out.Duplicates = out.Occurrences - out.Unique
	return out, nil
}
