package serverstore

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// AnonymousAnalyticsStore is optional; absence means unavailable, not zero.
// A client is a retained pseudonymous installation, never a verified person.
type AnonymousAnalyticsStore interface {
	RecordAnonymousClient(context.Context, string, time.Time) error
	AnonymousAnalytics(context.Context, time.Time) (AnonymousAnalytics, error)
}

type AnonymousAnalytics struct {
	NRU, DAU, MAU, TotalClients int64
	CollectedSince              time.Time
	Daily                       []AnonymousDailyMetric
	Cohorts                     []AnonymousCohort
}
type AnonymousDailyMetric struct {
	Day           time.Time
	NRU, DAU, MAU int64
	Requests      int64
}
type AnonymousCohort struct {
	Day       time.Time
	Size      int64
	Retention []AnonymousRetention
}
type AnonymousRetention struct {
	Day      int
	Active   int64
	Eligible bool
}
type anonymousClientRecord struct {
	FirstSeen, LastSeen time.Time
	RequestCount        int64
	Days                map[string]int64
}

func validAnonymousHash(s string) bool {
	if len(s) != 64 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func anonymousDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (f *Fake) RecordAnonymousClient(_ context.Context, hash string, now time.Time) error {
	if !validAnonymousHash(hash) {
		return errors.New("invalid anonymous client hash")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.anonymousClients == nil {
		f.anonymousClients = map[string]*anonymousClientRecord{}
	}
	now = now.UTC()
	r := f.anonymousClients[hash]
	if r == nil {
		r = &anonymousClientRecord{FirstSeen: now, LastSeen: now, Days: map[string]int64{}}
		f.anonymousClients[hash] = r
	}
	if now.Before(r.FirstSeen) {
		r.FirstSeen = now
	}
	if now.After(r.LastSeen) {
		r.LastSeen = now
	}
	r.RequestCount++
	r.Days[now.Format("2006-01-02")]++
	if f.anonymousStarted.IsZero() || now.Before(f.anonymousStarted) {
		f.anonymousStarted = now
	}
	return nil
}

func (f *Fake) AnonymousAnalytics(_ context.Context, now time.Time) (AnonymousAnalytics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	today := anonymousDay(now)
	out := AnonymousAnalytics{CollectedSince: f.anonymousStarted, TotalClients: int64(len(f.anonymousClients))}
	start := today.AddDate(0, 0, -89)
	if f.anonymousStarted.IsZero() {
		return out, nil
	}
	if d := anonymousDay(f.anonymousStarted); d.After(start) {
		start = d
	}
	for day := start; !day.After(today); day = day.AddDate(0, 0, 1) {
		m := AnonymousDailyMetric{Day: day}
		cohort := AnonymousCohort{Day: day}
		for _, offset := range []int{1, 7, 30} {
			cohort.Retention = append(cohort.Retention, AnonymousRetention{Day: offset, Eligible: day.AddDate(0, 0, offset).Before(today)})
		}
		for _, r := range f.anonymousClients {
			first := anonymousDay(r.FirstSeen)
			if first.Equal(day) {
				m.NRU++
				cohort.Size++
				for i, cell := range cohort.Retention {
					if cell.Eligible && r.Days[day.AddDate(0, 0, cell.Day).Format("2006-01-02")] > 0 {
						cohort.Retention[i].Active++
					}
				}
			}
			if r.Days[day.Format("2006-01-02")] > 0 {
				m.DAU++
				m.Requests += r.Days[day.Format("2006-01-02")]
			}
			for k := range r.Days {
				if k >= day.AddDate(0, 0, -29).Format("2006-01-02") && k <= day.Format("2006-01-02") {
					m.MAU++
					break
				}
			}
		}
		out.Daily = append(out.Daily, m)
		if cohort.Size > 0 {
			out.Cohorts = append(out.Cohorts, cohort)
		}
		if day.Equal(today) {
			out.NRU = m.NRU
			out.DAU = m.DAU
			out.MAU = m.MAU
		}
	}
	return out, nil
}

// PruneAnonymousAnalytics bounds identifiable history: daily rows 120 UTC
// days, summaries 365 days since last activity. A returning expired ID is new.
func (f *Fake) PruneAnonymousAnalytics(_ context.Context, now time.Time, limit int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var n int64
	for hash, r := range f.anonymousClients {
		if r.LastSeen.Before(now.AddDate(0, 0, -365)) && n < int64(limit) {
			delete(f.anonymousClients, hash)
			n++
			continue
		}
		for day := range r.Days {
			if day < anonymousDay(now).AddDate(0, 0, -119).Format("2006-01-02") && n < int64(limit) {
				delete(r.Days, day)
				n++
			}
		}
	}
	return n, nil
}
