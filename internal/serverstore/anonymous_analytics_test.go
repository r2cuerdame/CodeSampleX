package serverstore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func runAnonymousContract(t *testing.T, s AnonymousAnalyticsStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	today := anonymousDay(now)
	if p, ok := s.(*PG); ok {
		if err := p.withConn(ctx, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, "UPDATE anonymous_analytics_collection SET started_at=$1", today.AddDate(0, 0, -130))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordAnonymousClient(ctx, "bad", now); err == nil {
		t.Fatal("accepted invalid hash")
	}
	a, b, c, d := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	for _, event := range []struct {
		id string
		at time.Time
	}{
		{a, today.AddDate(0, 0, -31)}, {b, today.AddDate(0, 0, -31)},
		{a, today.AddDate(0, 0, -30)}, {a, today.AddDate(0, 0, -24)}, {a, today.AddDate(0, 0, -1)},
		{c, today.AddDate(0, 0, -29)}, {d, now},
		{d, today.Add(2 * time.Hour)}, // out of order, first_seen moves backwards
	} {
		if err := s.RecordAnonymousClient(ctx, event.id, event.at); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := s.RecordAnonymousClient(ctx, d, now); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	m, err := s.AnonymousAnalytics(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if m.NRU != 1 || m.DAU != 1 || m.MAU != 3 || m.TotalClients != 4 {
		t.Fatalf("wrong boundaries %+v", m)
	}
	var found bool
	for _, co := range m.Cohorts {
		if co.Day.Equal(today.AddDate(0, 0, -31)) {
			found = true
			if co.Size != 2 {
				t.Fatal(co)
			}
			for _, r := range co.Retention {
				if !r.Eligible || r.Active != 1 {
					t.Fatalf("retention %+v", co)
				}
			}
		}
		if co.Day.Equal(today) {
			for _, r := range co.Retention {
				if r.Eligible {
					t.Fatal("immature cohort eligible")
				}
			}
		}
	}
	if !found {
		t.Fatal("missing mature cohort")
	}
	if len(m.Daily) == 0 || m.Daily[len(m.Daily)-1].NRU != 1 {
		t.Fatal("missing latest day")
	}
	// Current return day is not complete: D30 must remain censored.
	if err := s.RecordAnonymousClient(ctx, strings.Repeat("e", 64), today.AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	m, err = s.AnonymousAnalytics(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, co := range m.Cohorts {
		if co.Day.Equal(today.AddDate(0, 0, -30)) && co.Retention[2].Eligible {
			t.Fatal("partial current day included in retention")
		}
	}
	// Inspect measured storage invariants in addition to aggregate outputs.
	switch st := s.(type) {
	case *Fake:
		r := st.anonymousClients[d]
		if r.RequestCount != 14 || !r.FirstSeen.Equal(today.Add(2*time.Hour)) || !r.LastSeen.Equal(now) {
			t.Fatal(r)
		}
	case *PG:
		err := st.withConn(ctx, func(conn *pgx.Conn) error {
			var n, daily int64
			var first, last time.Time
			err := conn.QueryRow(ctx, `SELECT first_seen,last_seen,request_count,(SELECT request_count FROM anonymous_client_days WHERE client_hash=$1 AND day=$2) FROM anonymous_clients WHERE client_hash=$1`, d, today).Scan(&first, &last, &n, &daily)
			if err != nil {
				return err
			}
			if n != 14 || daily != 14 || !first.Equal(today.Add(2*time.Hour)) || !last.Equal(now) {
				return fmt.Errorf("counters %d/%d first %s last %s", n, daily, first, last)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestAnonymousFakeContract(t *testing.T)          { runAnonymousContract(t, NewFake()) }
func TestIntegrationAnonymousPGContract(t *testing.T) { runAnonymousContract(t, openTestPG(t)) }

func TestIntegrationAnonymousMigrationAndPruning(t *testing.T) {
	p := openTestPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := p.Migrate(ctx); err != nil {
		t.Fatal("migration rerun", err)
	}
	for i, days := range []int{-366, -121, -119, -89, -1} {
		if err := p.RecordAnonymousClient(ctx, fmt.Sprintf("%064x", i+1), anonymousDay(now).AddDate(0, 0, days)); err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		if _, err := p.PruneAnonymousAnalytics(ctx, now, 1); err != nil {
			t.Fatal(err)
		}
	}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var clients, days int
		err := c.QueryRow(ctx, `SELECT (SELECT count(*) FROM anonymous_clients),(SELECT count(*) FROM anonymous_client_days)`).Scan(&clients, &days)
		if err != nil {
			return err
		}
		if clients != 4 || days != 3 {
			return fmt.Errorf("pruned counts %d %d", clients, days)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
