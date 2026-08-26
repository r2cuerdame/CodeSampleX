package serverstore

import (
	"context"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var _ AnomalyStore = (*Fake)(nil)

// The fake is the reference implementation of the dedupe semantics pg.go is
// held to, exactly as it is for evidence merging. The rule that matters is
// one line long and is the whole spam defence: a fingerprint already held
// returns the row it already has, and never a second one.

func (f *Fake) RecordAnomalyReport(_ context.Context, row AnomalyReportRow, now time.Time) (AnomalyReportRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.anomalies == nil {
		f.anomalies = map[string]*AnomalyReportRow{}
	}
	if existing, ok := f.anomalies[row.Fingerprint]; ok {
		existing.Reports++
		existing.LastSeen = now
		return *existing, true, nil
	}
	f.nextAnomalyID++
	row.ID = f.nextAnomalyID
	row.Reports = 1
	row.FirstSeen = now
	row.LastSeen = now
	if row.Status == "" {
		row.Status = domain.AnomalyStatusQueued
	}
	stored := row
	f.anomalies[row.Fingerprint] = &stored
	f.anomalyOrder = append(f.anomalyOrder, row.Fingerprint)
	return stored, false, nil
}

func (f *Fake) findAnomalyLocked(id int64) *AnomalyReportRow {
	for _, row := range f.anomalies {
		if row.ID == id {
			return row
		}
	}
	return nil
}

func (f *Fake) AnomalyReportByID(_ context.Context, id int64) (AnomalyReportRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.findAnomalyLocked(id)
	if row == nil {
		return AnomalyReportRow{}, false, nil
	}
	return *row, true, nil
}

func (f *Fake) AttachAnomalyVerificationJob(_ context.Context, id, jobID int64, status, unsupportedReason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.findAnomalyLocked(id)
	if row == nil {
		return nil
	}
	row.JobID = jobID
	row.Status = status
	row.UnsupportedReason = unsupportedReason
	return nil
}

func (f *Fake) SetAnomalyVerdict(_ context.Context, id int64, verdict string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.findAnomalyLocked(id)
	if row == nil || row.Verdict != "" {
		return false, nil
	}
	row.Verdict = verdict
	row.Status = domain.AnomalyStatusVerified
	row.VerdictAt = at
	return true, nil
}

func (f *Fake) OpenAnomalyReportsForSample(_ context.Context, sampleID string) ([]AnomalyReportRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AnomalyReportRow
	for _, row := range f.anomalies {
		if row.SampleID == sampleID && row.Verdict == "" {
			out = append(out, *row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *Fake) ListAnomalyReports(_ context.Context, limit int) ([]AnomalyReportRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AnomalyReportRow
	for _, row := range f.anomalies {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *Fake) AnomalyInsights(_ context.Context, now time.Time, days int) (AnomalyInsights, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start := now.AddDate(0, 0, -days)
	out := AnomalyInsights{WindowStart: start, WindowEnd: now}
	verdicts := map[string]int64{}
	perReporter := map[string]int64{}
	for _, row := range f.anomalies {
		if row.LastSeen.Before(start) {
			continue
		}
		out.Reports += row.Reports
		out.Unique++
		perReporter[row.ReporterBucket] += row.Reports
		switch row.Status {
		case domain.AnomalyStatusQueued:
			out.Queued++
		case domain.AnomalyStatusVerifying:
			out.Verifying++
		case domain.AnomalyStatusVerified:
			out.Verified++
		case domain.AnomalyStatusUnsupported:
			out.Unsupported++
		}
		if row.Verdict != "" {
			verdicts[row.Verdict]++
			if domain.AnomalyVerdictConfirmed(row.Verdict) {
				out.Confirmed++
			}
			if row.Verdict == domain.AnomalyVerdictCSXDefect {
				out.CSXDefects++
			}
			if row.Verdict == domain.AnomalyVerdictInsufficientEvidence {
				out.Insufficient++
			}
			if !row.VerdictAt.IsZero() && !row.FirstSeen.IsZero() {
				latency := row.VerdictAt.Sub(row.FirstSeen)
				if latency < 0 {
					latency = 0
				}
				out.VerdictLatencyTotal += latency
				out.VerdictLatencyCount++
				if latency > out.VerdictLatencyMax {
					out.VerdictLatencyMax = latency
				}
			}
		}
	}
	out.Duplicates = out.Reports - out.Unique
	for verdict, count := range verdicts {
		out.Verdicts = append(out.Verdicts, AnomalyVerdictCounts{Verdict: verdict, Count: count})
	}
	sort.Slice(out.Verdicts, func(i, j int) bool {
		if out.Verdicts[i].Count != out.Verdicts[j].Count {
			return out.Verdicts[i].Count > out.Verdicts[j].Count
		}
		return out.Verdicts[i].Verdict < out.Verdicts[j].Verdict
	})
	for _, count := range perReporter {
		if count > out.BusiestReporter {
			out.BusiestReporter = count
		}
	}
	return out, nil
}
