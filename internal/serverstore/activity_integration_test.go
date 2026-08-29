package serverstore

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/activity"
)

func activityTestMonth(now time.Time, offset int) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month()+time.Month(offset), 1, 12, 0, 0, 0, time.UTC)
}

func TestActivityRetentionMonthFixtureUsesCalendarMonths(t *testing.T) {
	anchor := time.Date(2026, time.August, 31, 23, 0, 0, 0, time.UTC)
	epochs := make(map[string]struct{}, 13)
	for age := 0; age <= 12; age++ {
		epochs[activityTestMonth(anchor, -age).Format("2006-01")] = struct{}{}
	}
	if len(epochs) != 13 {
		t.Fatalf("month-end fixture produced %d distinct epochs, want 13", len(epochs))
	}
	for _, epoch := range []string{"2026-08", "2026-02", "2025-08"} {
		if _, ok := epochs[epoch]; !ok {
			t.Fatalf("month-end fixture omitted %s: %v", epoch, epochs)
		}
	}
}

func TestPGActivityBucketsIdempotenceOwnerExclusionAndRetention(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	clock := time.Now().UTC()
	now := time.Date(clock.Year(), clock.Month(), clock.Day(), 12, 0, 0, 0, time.UTC)
	dayEpoch, monthEpoch := now.Format("2006-01-02"), now.Format("2006-01")
	bucket := func(n byte) [16]byte { var b [16]byte; b[0] = n; return b }

	rows := []activity.Bucket{
		{Kind: activity.KindDay, Epoch: dayEpoch, Value: bucket(1), SeenAt: now},
		{Kind: activity.KindMonth, Epoch: monthEpoch, Value: bucket(2), SeenAt: now},
		{Kind: activity.KindDay, Epoch: dayEpoch, Value: bucket(3), Owner: true, SeenAt: now},
		{Kind: activity.KindMonth, Epoch: monthEpoch, Value: bucket(4), Owner: true, SeenAt: now},
	}
	if err := pg.RecordActivity(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := pg.RecordActivity(ctx, rows); err != nil {
		t.Fatalf("duplicate upsert: %v", err)
	}
	counts, err := pg.ActivityCounts(ctx, dayEpoch, monthEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if counts.ExternalDAU != 1 || counts.ExternalMAU != 1 || counts.OwnerDAU != 1 || counts.OwnerMAU != 1 || !counts.DaySeen || !counts.MonthSeen {
		t.Fatalf("counts after duplicate = %+v, want one external and owner excluded", counts)
	}

	// Owner classification is monotonic and therefore retroactive.
	if err := pg.RecordActivity(ctx, []activity.Bucket{
		{Kind: activity.KindDay, Epoch: dayEpoch, Value: bucket(1), Owner: true, SeenAt: now.Add(time.Minute)},
		{Kind: activity.KindMonth, Epoch: monthEpoch, Value: bucket(2), Owner: true, SeenAt: now.Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	counts, err = pg.ActivityCounts(ctx, dayEpoch, monthEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if counts.ExternalDAU != 0 || counts.ExternalMAU != 0 || counts.OwnerDAU != 2 || counts.OwnerMAU != 2 {
		t.Fatalf("retroactive owner counts = %+v", counts)
	}

	retentionRows := make([]activity.Bucket, 0, 50)
	for age := 0; age <= 34; age++ {
		seenAt := now.AddDate(0, 0, -age)
		retentionRows = append(retentionRows, activity.Bucket{
			Kind: activity.KindDay, Epoch: seenAt.Format("2006-01-02"), Value: bucket(byte(20 + age)), SeenAt: seenAt,
		})
	}
	for age := 0; age <= 12; age++ {
		// Subtract from the first of the month. time.AddDate preserves the day
		// and normalizes an invalid date, so subtracting from August 29-31 can
		// turn February into March and silently collapse two calendar epochs.
		seenAt := activityTestMonth(now, -age)
		retentionRows = append(retentionRows, activity.Bucket{
			Kind: activity.KindMonth, Epoch: seenAt.Format("2006-01"), Value: bucket(byte(80 + age)), SeenAt: seenAt,
		})
	}
	if err := pg.RecordActivity(ctx, retentionRows); err != nil {
		t.Fatal(err)
	}
	staleDay, staleMonth := bucket(55), bucket(95)
	staleDayEpoch := now.AddDate(0, 0, -35).Format("2006-01-02")
	staleMonthEpoch := activityTestMonth(now, -13).Format("2006-01")
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `INSERT INTO activity_buckets(kind, epoch, bucket, owner, first_seen, last_seen) VALUES
			('day',$1,$2,false,$4,$4), ('month',$3,$5,false,$4,$4)`, staleDayEpoch, staleDay[:], staleMonthEpoch, now, staleMonth[:])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := pg.MarkActivityHealthy(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := pg.PruneActivity(ctx, now); err != nil {
		t.Fatal(err)
	}
	var staleCount, dayEpochs, monthEpochs int
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT
			COUNT(*) FILTER (WHERE (kind='day' AND epoch=$1) OR (kind='month' AND epoch=$2)),
			COUNT(DISTINCT epoch) FILTER (WHERE kind='day'),
			COUNT(DISTINCT epoch) FILTER (WHERE kind='month')
			FROM activity_buckets`, staleDayEpoch, staleMonthEpoch).Scan(&staleCount, &dayEpochs, &monthEpochs)
	}); err != nil {
		t.Fatal(err)
	}
	if staleCount != 0 || dayEpochs != 35 || monthEpochs != 13 {
		t.Fatalf("retention stale=%d daily=%d monthly=%d, want 0/35/13 including current", staleCount, dayEpochs, monthEpochs)
	}
}

func TestPGActivityConcurrentReversedBatchesDoNotDeadlock(t *testing.T) {
	pg := openTestPG(t)
	clock := time.Now().UTC()
	now := time.Date(clock.Year(), clock.Month(), clock.Day(), 12, 0, 0, 0, time.UTC)
	rows := make([]activity.Bucket, 0, 12)
	for i := 0; i < 6; i++ {
		var value [16]byte
		value[0] = byte(i + 1)
		rows = append(rows,
			activity.Bucket{Kind: activity.KindDay, Epoch: now.Format("2006-01-02"), Value: value, SeenAt: now},
			activity.Bucket{Kind: activity.KindMonth, Epoch: now.Format("2006-01"), Value: value, SeenAt: now},
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			batch := append([]activity.Bucket(nil), rows...)
			if worker%2 == 1 {
				for i, j := 0, len(batch)-1; i < j; i, j = i+1, j-1 {
					batch[i], batch[j] = batch[j], batch[i]
				}
			}
			for attempt := 0; attempt < 10; attempt++ {
				if err := pg.RecordActivity(ctx, batch); err != nil {
					errs <- fmt.Errorf("worker %d iteration %d: %w", worker, attempt, err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestActivityMigrationHasOnlyBoundedNonPIIColumns(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	var columns []string
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='activity_buckets' ORDER BY ordinal_position`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			columns = append(columns, name)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"kind", "epoch", "bucket", "owner", "first_seen", "last_seen"}
	if len(columns) != len(want) {
		t.Fatalf("columns = %v, want %v", columns, want)
	}
	for i := range want {
		if columns[i] != want[i] {
			t.Fatalf("columns = %v, want %v", columns, want)
		}
	}
	columns = nil
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='activity_health' ORDER BY ordinal_position`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			columns = append(columns, name)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(columns, []string{"epoch", "checked_at"}) {
		t.Fatalf("activity health columns = %v, want only bounded epoch health", columns)
	}
}

func TestPGActivityRejectsMalformedEpochsBeforePersistence(t *testing.T) {
	pg := openTestPG(t)
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	for _, row := range []activity.Bucket{
		{Kind: activity.KindDay, Epoch: "forever", SeenAt: now},
		{Kind: activity.KindMonth, Epoch: "2026-99", SeenAt: now},
		{Kind: "stable", Epoch: "2026", SeenAt: now},
		{Kind: activity.KindDay, Epoch: "2026-08-16", SeenAt: now},
		{Kind: activity.KindMonth, Epoch: "2026-07", SeenAt: now},
	} {
		if err := pg.RecordActivity(context.Background(), []activity.Bucket{row}); err == nil {
			t.Fatalf("malformed activity row accepted: kind=%q epoch=%q", row.Kind, row.Epoch)
		}
	}
}

func TestPGActivityRejectsBacklogAndFutureAndPrunesBothTails(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	bucket := func(n byte) [16]byte { var b [16]byte; b[0] = n; return b }

	// One transaction containing an otherwise-valid row and a future row must
	// roll back atomically; retrying it can never create a partial estimate.
	currentDay := now.Format("2006-01-02")
	b201 := bucket(201)
	err := pg.RecordActivity(ctx, []activity.Bucket{
		{Kind: activity.KindDay, Epoch: currentDay, Value: b201, SeenAt: now},
		{Kind: activity.KindDay, Epoch: now.AddDate(0, 0, 1).Format("2006-01-02"), Value: bucket(202), SeenAt: now.AddDate(0, 0, 1)},
	})
	if err == nil {
		t.Fatal("valid-formatted future day was accepted")
	}
	var partial int
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT COUNT(*) FROM activity_buckets WHERE kind='day' AND epoch=$1 AND bucket=$2`, currentDay, b201[:]).Scan(&partial)
	}); err != nil {
		t.Fatal(err)
	}
	if partial != 0 {
		t.Fatal("future-epoch rejection left a partial transaction behind")
	}

	for _, row := range []activity.Bucket{
		{Kind: activity.KindDay, Epoch: now.AddDate(0, 0, -35).Format("2006-01-02"), Value: bucket(203), SeenAt: now.AddDate(0, 0, -35)},
		{Kind: activity.KindMonth, Epoch: activityTestMonth(now, -13).Format("2006-01"), Value: bucket(204), SeenAt: activityTestMonth(now, -13)},
		{Kind: activity.KindMonth, Epoch: activityTestMonth(now, 1).Format("2006-01"), Value: bucket(205), SeenAt: activityTestMonth(now, 1)},
	} {
		if err := pg.RecordActivity(ctx, []activity.Bucket{row}); err == nil {
			t.Fatalf("out-of-window valid epoch accepted: kind=%q epoch=%q", row.Kind, row.Epoch)
		}
	}

	// Simulate rows left by an older binary or a skewed database clock and
	// prove the current pruning pass removes old and future epochs alike.
	b206 := bucket(206)
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `INSERT INTO activity_buckets(kind, epoch, bucket, owner, first_seen, last_seen) VALUES
			('day',$1,$2,false,$5,$5), ('day',$3,$2,false,$5,$5),
			('month',$4,$2,false,$5,$5)`,
			now.AddDate(0, 0, -35).Format("2006-01-02"), b206[:],
			now.AddDate(0, 0, 1).Format("2006-01-02"), activityTestMonth(now, 1).Format("2006-01"), now); err != nil {
			return err
		}
		_, err := c.Exec(ctx, `INSERT INTO activity_health(epoch, checked_at) VALUES ($1,$3),($2,$3)`,
			now.AddDate(0, 0, -35).Format("2006-01-02"), now.AddDate(0, 0, 1).Format("2006-01-02"), now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := pg.MarkActivityHealthy(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := pg.PruneActivity(ctx, now); err != nil {
		t.Fatal(err)
	}
	var outside, healthOutside, healthCurrent int
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT
			(SELECT COUNT(*) FROM activity_buckets WHERE
				(kind='day' AND (epoch < $1 OR epoch > $2)) OR
				(kind='month' AND (epoch < $3 OR epoch > $4))),
			(SELECT COUNT(*) FROM activity_health WHERE epoch < $1 OR epoch > $2),
			(SELECT COUNT(*) FROM activity_health WHERE epoch=$2)`,
			now.AddDate(0, 0, -34).Format("2006-01-02"), currentDay,
			activityTestMonth(now, -12).Format("2006-01"), activityTestMonth(now, 0).Format("2006-01")).Scan(&outside, &healthOutside, &healthCurrent)
	}); err != nil {
		t.Fatal(err)
	}
	if outside != 0 || healthOutside != 0 || healthCurrent != 1 {
		t.Fatalf("prune retained buckets=%d healthOutside=%d healthCurrent=%d, want 0/0/1", outside, healthOutside, healthCurrent)
	}
}

// The daily chart is only honest if the store can tell three cases apart: a
// day with external activity, a day whose only buckets were the owner's, and
// a day with nothing stored at all. It also has to report the oldest retained
// epoch, which is what separates "collection had not started" from "zero".
func TestPGActivityDailySeparatesOwnerOnlyDaysFromUncollectedDays(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	clock := time.Now().UTC()
	now := time.Date(clock.Year(), clock.Month(), clock.Day(), 12, 0, 0, 0, time.UTC)
	bucket := func(n byte) [16]byte { var b [16]byte; b[0] = n; return b }
	day := func(offset int) string { return now.AddDate(0, 0, offset).Format("2006-01-02") }

	rows := []activity.Bucket{
		{Kind: activity.KindDay, Epoch: day(-4), Value: bucket(1), SeenAt: now.AddDate(0, 0, -4)},
		{Kind: activity.KindDay, Epoch: day(-4), Value: bucket(2), SeenAt: now.AddDate(0, 0, -4)},
		// day(-3) has no request buckets; its health marker proves a real zero.
		{Kind: activity.KindDay, Epoch: day(-2), Value: bucket(3), Owner: true, SeenAt: now.AddDate(0, 0, -2)},
		// day(-1) is deliberately absent: a real collection gap.
		{Kind: activity.KindDay, Epoch: day(0), Value: bucket(4), SeenAt: now},
		// A month bucket must never leak into the daily series.
		{Kind: activity.KindMonth, Epoch: now.Format("2006-01"), Value: bucket(5), SeenAt: now},
	}
	for _, offset := range []int{-4, -3, 0} {
		if err := pg.MarkActivityHealthy(ctx, now.AddDate(0, 0, offset)); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.RecordActivity(ctx, rows); err != nil {
		t.Fatal(err)
	}

	from, to := day(-(activity.DailyWindowDays - 1)), day(0)
	raw, err := pg.ActivityDaily(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if raw.OldestEpoch != day(-4) {
		t.Fatalf("oldest retained day = %q, want %q", raw.OldestEpoch, day(-4))
	}
	got := make(map[string]activity.DayCount, len(raw.Days))
	for _, d := range raw.Days {
		got[d.Epoch] = d
	}
	if len(got) != 4 {
		t.Fatalf("daily rows = %+v, want exactly 4 stored days", raw.Days)
	}
	if d := got[day(-4)]; d.Count != 2 || d.Rows != 2 || !d.Healthy {
		t.Errorf("external day = %+v, want count 2 rows 2", d)
	}
	if d, ok := got[day(-3)]; !ok || d.Count != 0 || d.Rows != 0 || !d.Healthy {
		t.Errorf("healthy zero day = %+v ok=%v", d, ok)
	}
	if d, ok := got[day(-2)]; !ok || d.Count != 0 || d.OwnerExcluded != 1 || d.Rows != 1 {
		t.Errorf("owner-only day = %+v ok=%v, want a stored row with zero external", d, ok)
	}
	if _, ok := got[day(-1)]; ok {
		t.Errorf("uncollected day %s produced a row", day(-1))
	}
	if d := got[day(0)]; d.Count != 1 || d.Rows != 1 {
		t.Errorf("today = %+v, want count 1 rows 1", d)
	}

	window := activity.BuildDailyWindow(now, raw)
	if len(window.Points) != activity.DailyWindowDays || window.Gaps != 1 || window.Max != 2 || window.StartEpoch != day(-4) {
		t.Fatalf("window = points:%d gaps:%d max:%d start:%s", len(window.Points), window.Gaps, window.Max, window.StartEpoch)
	}
	for _, p := range window.Points {
		switch p.Epoch {
		case day(-1):
			if !p.Gap {
				t.Errorf("%s should be a gap, got %+v", p.Epoch, p)
			}
		case day(-3):
			if !p.HealthyZero || p.Gap || p.BeforeCollection || p.Count != 0 {
				t.Errorf("healthy zero %s = %+v", p.Epoch, p)
			}
		case day(-2):
			if p.Gap || p.BeforeCollection || p.Count != 0 {
				t.Errorf("owner-only %s should be a collected zero, got %+v", p.Epoch, p)
			}
		}
	}

	if _, err := pg.ActivityDaily(ctx, "2026-8-1", to); err == nil {
		t.Fatal("malformed day epoch range was accepted")
	}
}
