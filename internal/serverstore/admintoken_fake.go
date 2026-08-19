package serverstore

import (
	"context"
	"errors"
	"sort"
	"time"
)

func (f *Fake) IssueAdminTokens(_ context.Context, rows []AdminTokenRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.adminTokens == nil {
		f.adminTokens = make(map[string]AdminTokenRow)
	}
	live := 0
	for _, row := range f.adminTokens {
		if row.RevokedAt.IsZero() {
			live++
		}
	}
	if live+len(rows) > MaxAdminTokens {
		return errors.New("serverstore: too many live admin tokens")
	}
	for _, row := range rows {
		if row.TokenHash == "" || row.TokenID == "" {
			return errors.New("serverstore: admin token needs a digest and an id")
		}
		f.adminTokens[row.TokenHash] = row
	}
	return nil
}

func (f *Fake) ResolveAdminToken(_ context.Context, tokenHash, ip string, now time.Time) (AdminTokenRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.adminTokens[tokenHash]
	if !ok || !row.Live(now) {
		return AdminTokenRow{}, false, nil
	}
	row.LastUsedAt, row.LastUsedIP = now, ip
	f.adminTokens[tokenHash] = row
	return row, true, nil
}

func (f *Fake) ListAdminTokens(_ context.Context, limit int) ([]AdminTokenRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AdminTokenRow, 0, len(f.adminTokens))
	for _, row := range f.adminTokens {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].IssuedAt.Equal(out[j].IssuedAt) {
			return out[i].IssuedAt.After(out[j].IssuedAt)
		}
		return out[i].TokenID < out[j].TokenID
	})
	if limit < 1 || limit > MaxAdminTokens {
		limit = MaxAdminTokens
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *Fake) RevokeAdminToken(_ context.Context, tokenID string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for hash, row := range f.adminTokens {
		if row.TokenID != tokenID || !row.RevokedAt.IsZero() {
			continue
		}
		row.RevokedAt = now
		f.adminTokens[hash] = row
		return true, nil
	}
	return false, nil
}

var _ AdminTokenStore = (*Fake)(nil)
