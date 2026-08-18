package serverstore

import (
	"context"
	"sort"
	"time"
)

func (f *Fake) IssueAuthoringSessions(_ context.Context, rows []AuthoringSessionRow, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	active := 0
	for _, row := range f.authoring {
		if row.RevokedAt.IsZero() && now.Before(row.IdleExpiresAt) {
			active++
		}
	}
	if active+len(rows) > MaxAuthoringSessions {
		return ErrAuthoringSessionLimit
	}
	seenTokens := make(map[string]struct{}, len(rows))
	seenSessions := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, exists := f.authoring[row.TokenHash]; exists {
			return ErrAuthoringSessionMissing
		}
		if _, exists := seenTokens[row.TokenHash]; exists {
			return ErrAuthoringSessionMissing
		}
		for _, existing := range f.authoring {
			if existing.SessionID == row.SessionID {
				return ErrAuthoringSessionMissing
			}
		}
		if _, exists := seenSessions[row.SessionID]; exists {
			return ErrAuthoringSessionMissing
		}
		seenTokens[row.TokenHash] = struct{}{}
		seenSessions[row.SessionID] = struct{}{}
	}
	for _, row := range rows {
		f.authoring[row.TokenHash] = row
	}
	return nil
}

func (f *Fake) RotateAuthoringSession(_ context.Context, sessionID, tokenHash string, now, idleExpiresAt time.Time) (AuthoringSessionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.authoring[tokenHash]; exists {
		return AuthoringSessionRow{}, ErrAuthoringSessionMissing
	}
	for oldHash, row := range f.authoring {
		if row.SessionID != sessionID || !row.RevokedAt.IsZero() || !now.Before(row.IdleExpiresAt) {
			continue
		}
		delete(f.authoring, oldHash)
		row.TokenHash = tokenHash
		row.LastRefreshAt = time.Time{}
		row.IdleExpiresAt = idleExpiresAt
		row.LastRefreshIP = ""
		row.ComputerName = ""
		f.authoring[tokenHash] = row
		return row, nil
	}
	return AuthoringSessionRow{}, ErrAuthoringSessionMissing
}

func (f *Fake) RefreshAuthoringSession(_ context.Context, tokenHash, ip, computerName string, now, idleExpiresAt time.Time) (AuthoringSessionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.authoring[tokenHash]
	if !ok || !row.RevokedAt.IsZero() {
		return AuthoringSessionRow{}, ErrAuthoringSessionMissing
	}
	if !now.Before(row.IdleExpiresAt) {
		return AuthoringSessionRow{}, ErrAuthoringSessionExpired
	}
	if row.LastRefreshAt.IsZero() || !row.LastRefreshAt.After(now.Add(-5*time.Minute)) {
		row.LastRefreshAt = now
		row.IdleExpiresAt = idleExpiresAt
		if ip != "" {
			row.LastRefreshIP = ip
		}
		if computerName != "" {
			row.ComputerName = computerName
		}
		f.authoring[tokenHash] = row
	}
	return row, nil
}

func (f *Fake) RevokeAuthoringSession(_ context.Context, sessionID string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, row := range f.authoring {
		if row.SessionID == sessionID && row.RevokedAt.IsZero() {
			row.RevokedAt = now
			f.authoring[key] = row
			return true, nil
		}
	}
	return false, nil
}

func (f *Fake) ListAuthoringSessions(_ context.Context, now time.Time, limit int) ([]AuthoringSessionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AuthoringSessionRow
	for _, row := range f.authoring {
		if row.RevokedAt.IsZero() && now.Before(row.IdleExpiresAt) {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IssuedAt.Equal(out[j].IssuedAt) {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].IssuedAt.After(out[j].IssuedAt)
	})
	if limit < 1 || limit > MaxAuthoringSessions {
		limit = MaxAuthoringSessions
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

var _ AuthoringSessionStore = (*Fake)(nil)

func (f *Fake) SaveAuthoringDraft(_ context.Context, row AuthoringDraftRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.authoringDrafts[row.SampleID]; ok && !existing.CreatedAt.IsZero() {
		row.CreatedAt = existing.CreatedAt
	}
	f.authoringDrafts[row.SampleID] = row
	return nil
}

func (f *Fake) ListAuthoringDrafts(_ context.Context, limit int) ([]AuthoringDraftRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AuthoringDraftRow, 0, len(f.authoringDrafts))
	for _, row := range f.authoringDrafts {
		row.VerificationStatus = "PENDING"
		if sample, ok := f.samples[row.SampleID]; ok && sample.Status == "CROSS_PASS" && !sample.Quarantined {
			row.VerificationStatus = "CROSS_PASS"
		} else {
			for _, receipt := range f.receipts[row.SampleID] {
				if receipt.ContractResult == "FAIL" {
					row.VerificationStatus = "CROSS_FAIL"
				}
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].SampleID < out[j].SampleID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit < 1 || limit > 100 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func authoringWorkKey(ecosystem, name, version, symbol string) [4]string {
	return [4]string{ecosystem, name, version, symbol}
}

func (f *Fake) ClaimAuthoringWork(_ context.Context, sessionID string, candidates []WantedRow, now, leaseExpiresAt time.Time) (AuthoringWorkRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, work := range f.authoringWork {
		if work.SampleID == "" && !now.Before(work.LeaseExpiresAt) {
			delete(f.authoringWork, key)
			continue
		}
		if work.SessionID == sessionID && work.SampleID == "" && now.Before(work.LeaseExpiresAt) {
			return work, true, nil
		}
	}
	for _, candidate := range candidates {
		key := authoringWorkKey(candidate.Ecosystem, candidate.Name, candidate.Version, candidate.Symbol)
		if _, exists := f.authoringWork[key]; exists {
			continue
		}
		work := AuthoringWorkRow{Ecosystem: candidate.Ecosystem, Name: candidate.Name, Version: candidate.Version,
			Symbol: candidate.Symbol, Asks: candidate.Asks, SessionID: sessionID, ClaimedAt: now, LeaseExpiresAt: leaseExpiresAt}
		f.authoringWork[key] = work
		return work, true, nil
	}
	return AuthoringWorkRow{}, false, nil
}

func (f *Fake) AuthoringWorkForSubmission(_ context.Context, sessionID, sampleID string, now time.Time) (AuthoringWorkRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, work := range f.authoringWork {
		if work.SampleID == "" && !now.Before(work.LeaseExpiresAt) {
			delete(f.authoringWork, key)
			continue
		}
		if work.SessionID == sessionID && ((work.SampleID == "" && now.Before(work.LeaseExpiresAt)) || work.SampleID == sampleID) {
			return work, true, nil
		}
	}
	return AuthoringWorkRow{}, false, nil
}

func (f *Fake) AttachAuthoringWorkSample(_ context.Context, sessionID string, work AuthoringWorkRow, sampleID string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := authoringWorkKey(work.Ecosystem, work.Name, work.Version, work.Symbol)
	current, ok := f.authoringWork[key]
	if !ok || current.SessionID != sessionID || current.SampleID != "" || !now.Before(current.LeaseExpiresAt) {
		return false, nil
	}
	current.SampleID = sampleID
	f.authoringWork[key] = current
	return true, nil
}
