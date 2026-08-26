package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// flowNow is a fixed instant the seeded rows are placed around, so every
// window boundary in this file is arithmetic rather than a race with the
// clock. AdminInsights takes its own `now`, which is what makes that possible.
var flowNow = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

func seedFlowFixture(t *testing.T, pg *PG) {
	t.Helper()
	ctx := context.Background()

	// Two samples: one accepted and serving, one still held. Both were
	// uploaded inside the last hour, so "produced" and "usable" are
	// distinguishable only if the query keeps them apart.
	for _, s := range []struct {
		id          string
		at          time.Time
		quarantined bool
	}{
		{"sha256:flow-live-hour", flowNow.Add(-20 * time.Minute), false},
		{"sha256:flow-held-hour", flowNow.Add(-25 * time.Minute), true},
		{"sha256:flow-live-day", flowNow.Add(-6 * time.Hour), false},
		{"sha256:flow-live-week", flowNow.Add(-50 * time.Hour), false},
		{"sha256:flow-live-old", flowNow.Add(-30 * 24 * time.Hour), false},
	} {
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, `
				INSERT INTO samples(sample_id, manifest, status, size_bytes, created_at, updated_at, quarantined)
				VALUES($1, '{}'::jsonb, 'PUBLISHED', 1, $2, $2, $3)`, s.id, s.at, s.quarantined)
			return err
		}); err != nil {
			t.Fatalf("seed sample %s: %v", s.id, err)
		}
	}

	// Completed verifications. PASS is not the throughput number: a FAIL is a
	// verifier doing its job, and counting only passes reports a stalled farm
	// and a failing one as the same zero.
	for _, r := range []struct {
		id     string
		at     time.Time
		result string
	}{
		{"flow-r-hour-pass", flowNow.Add(-10 * time.Minute), "PASS"},
		{"flow-r-hour-fail", flowNow.Add(-30 * time.Minute), "FAIL"},
		{"flow-r-hour-skip", flowNow.Add(-45 * time.Minute), "SKIPPED"},
		{"flow-r-prev-hour", flowNow.Add(-90 * time.Minute), "PASS"},
		{"flow-r-day", flowNow.Add(-8 * time.Hour), "PASS"},
		{"flow-r-day-blank", flowNow.Add(-9 * time.Hour), ""},
		{"flow-r-week", flowNow.Add(-100 * time.Hour), "PASS"},
		{"flow-r-old", flowNow.Add(-40 * 24 * time.Hour), "PASS"},
	} {
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, `
				INSERT INTO receipts(receipt_id, sample_id, peer_id, env_hash, receipt, contract_result, created_at)
				VALUES($1, 'sha256:flow-live-hour', 'peer', 'env', '{}'::jsonb, NULLIF($2,''), $3)`,
				r.id, r.result, r.at)
			return err
		}); err != nil {
			t.Fatalf("seed receipt %s: %v", r.id, err)
		}
	}

	// Search outcomes. Hits arrive already deduplicated per offer; misses are
	// written by RecordWantedBatch below so the retry path is exercised for
	// real rather than simulated.
	for _, h := range []struct {
		key string
		at  time.Time
	}{
		{"offer-hour-1", flowNow.Add(-5 * time.Minute)},
		{"offer-hour-2", flowNow.Add(-40 * time.Minute)},
		{"offer-hour-3", flowNow.Add(-50 * time.Minute)},
		{"offer-day-1", flowNow.Add(-5 * time.Hour)},
		{"offer-week-1", flowNow.Add(-60 * time.Hour)},
	} {
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, `
				INSERT INTO search_hits(grade, results_shown, sample_id, offer_id, epoch, anon_id, dedup_key, created_at)
				VALUES('EXACT', 1, 'sha256:flow-live-hour', $1, '2026-08-24', 'anon-hit', $1, $2)`,
				h.key, h.at)
			return err
		}); err != nil {
			t.Fatalf("seed search hit %s: %v", h.key, err)
		}
	}
}

// The three headline questions, answered from bounded windows the operator
// can act on. A 30-day cumulative figure cannot say whether the line is
// running right now, which is the only thing this panel exists to answer.
func TestIntegrationAdminFlowCountsEachWindowSeparately(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	seedFlowFixture(t, pg)

	insights, err := pg.AdminInsights(ctx, flowNow)
	if err != nil {
		t.Fatalf("AdminInsights: %v", err)
	}
	flow := insights.Flow

	for _, tc := range []struct {
		name                        string
		window                      AdminFlowWindow
		hits                        int64
		verifications               int64
		pass, fail, skip, unclassed int64
		accepted, held              int64
	}{
		{"hour", flow.Hour, 3, 3, 1, 1, 1, 0, 1, 1},
		{"day", flow.Day, 4, 6, 3, 1, 1, 1, 2, 1},
		{"week", flow.Week, 5, 7, 4, 1, 1, 1, 3, 1},
	} {
		if tc.window.Hits != tc.hits {
			t.Errorf("%s hits = %d, want %d", tc.name, tc.window.Hits, tc.hits)
		}
		if got := tc.window.Verifications.Total(); got != tc.verifications {
			t.Errorf("%s verifications = %d, want %d", tc.name, got, tc.verifications)
		}
		if v := tc.window.Verifications; v.Pass != tc.pass || v.Fail != tc.fail ||
			v.Skipped != tc.skip || v.Unclassified != tc.unclassed {
			t.Errorf("%s verification mix = pass:%d fail:%d skip:%d unclassified:%d, want %d/%d/%d/%d",
				tc.name, v.Pass, v.Fail, v.Skipped, v.Unclassified, tc.pass, tc.fail, tc.skip, tc.unclassed)
		}
		if tc.window.AcceptedSamples != tc.accepted {
			t.Errorf("%s accepted samples = %d, want %d", tc.name, tc.window.AcceptedSamples, tc.accepted)
		}
		if tc.window.HeldSamples != tc.held {
			t.Errorf("%s held samples = %d, want %d", tc.name, tc.window.HeldSamples, tc.held)
		}
	}

	// The hour before the current one, so "3 this hour" can be read as rising
	// or falling instead of as a number with nothing to compare it to.
	if got := flow.PreviousHour.Verifications.Total(); got != 1 {
		t.Errorf("previous hour verifications = %d, want 1", got)
	}
}

// A zero that means "nothing happened in the last hour" and a zero that means
// "the collector stopped three days ago" are different operational facts, and
// the panel cannot tell them apart without the last observed timestamps.
func TestIntegrationAdminFlowReportsWhenEachSourceLastMoved(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	seedFlowFixture(t, pg)

	flow, err := pg.AdminInsights(ctx, flowNow)
	if err != nil {
		t.Fatalf("AdminInsights: %v", err)
	}

	if !flow.Flow.HasLastVerification {
		t.Fatal("no last verification time, so a zero cannot be explained")
	}
	if want := flowNow.Add(-10 * time.Minute); !flow.Flow.LastVerification.Equal(want) {
		t.Errorf("last verification = %s, want %s", flow.Flow.LastVerification, want)
	}
	if !flow.Flow.HasLastSample {
		t.Fatal("no last accepted-sample time")
	}
	if want := flowNow.Add(-20 * time.Minute); !flow.Flow.LastSample.Equal(want) {
		t.Errorf("last accepted sample = %s, want %s", flow.Flow.LastSample, want)
	}
	if !flow.Flow.HasLastSearchOutcome {
		t.Fatal("no last search outcome time")
	}
	if want := flowNow.Add(-5 * time.Minute); !flow.Flow.LastSearchOutcome.Equal(want) {
		t.Errorf("last search outcome = %s, want %s", flow.Flow.LastSearchOutcome, want)
	}
}

// An empty database must produce no timestamps at all. Substituting the
// current time would turn "never collected" into "healthy a moment ago".
func TestIntegrationAdminFlowInventsNoTimestampsOnAnEmptyDatabase(t *testing.T) {
	pg := openTestPG(t)

	insights, err := pg.AdminInsights(context.Background(), flowNow)
	if err != nil {
		t.Fatalf("AdminInsights: %v", err)
	}
	flow := insights.Flow
	if flow.HasLastVerification || flow.HasLastSample || flow.HasLastSearchOutcome {
		t.Errorf("empty database reported activity: verification=%v sample=%v search=%v",
			flow.HasLastVerification, flow.HasLastSample, flow.HasLastSearchOutcome)
	}
	if flow.Hour.SearchTotal() != 0 || flow.Week.Verifications.Total() != 0 {
		t.Error("empty database produced nonzero flow counts")
	}
}

// The regression this counter exists to survive: an agent that retries the
// same search all afternoon is one question, not an afternoon of demand.
// wanted rows already deduplicate per reporter per UTC day; the miss counter
// has to deduplicate on the same key or the No-match rate becomes a measure of
// how often clients retry.
func TestIntegrationAdminFlowDoesNotCountARetriedMissTwice(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	epoch := flowNow.Format("2006-01-02")

	question := []WantedRow{
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post"},
		{Ecosystem: "npm", Name: "zod", Version: "3.23.8"},
	}
	// Same question, same reporter, same day, three times — and once more
	// with the coordinates reordered, which is what a rebuilt batch looks like.
	for i := 0; i < 3; i++ {
		if err := pg.RecordWanted(ctx, epoch, "anon-retry", question); err != nil {
			t.Fatalf("RecordWanted #%d: %v", i, err)
		}
	}
	reordered := []WantedRow{question[1], question[0]}
	if err := pg.RecordWanted(ctx, epoch, "anon-retry", reordered); err != nil {
		t.Fatalf("RecordWanted reordered: %v", err)
	}
	// A different question from the same reporter is a second data point.
	if err := pg.RecordWanted(ctx, epoch, "anon-retry", []WantedRow{
		{Ecosystem: "pypi", Name: "requests", Version: "2.32.3"},
	}); err != nil {
		t.Fatalf("RecordWanted second question: %v", err)
	}
	// And so is the same question from a different reporter.
	if err := pg.RecordWanted(ctx, epoch, "anon-other", question); err != nil {
		t.Fatalf("RecordWanted other reporter: %v", err)
	}

	insights, err := pg.AdminInsights(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("AdminInsights: %v", err)
	}
	if got := insights.Flow.Hour.NoMatches; got != 3 {
		t.Fatalf("no-match questions in the last hour = %d, want 3 "+
			"(one per distinct reporter-question, retries collapsed)", got)
	}
	// The demand ranking still counts coordinates, and must not have been
	// changed by giving the rate its own unit.
	rows, err := pg.TopWanted(ctx, 10)
	if err != nil {
		t.Fatalf("TopWanted: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("wanted coordinates = %d, want 3", len(rows))
	}
}

// A report that carries no coordinates is not a question and must not reach
// the denominator; the wire path already refuses to build one, and the
// counter must not invent one if it ever does.
func TestIntegrationAdminFlowIgnoresAnEmptyWantedReport(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	if err := pg.RecordWanted(ctx, flowNow.Format("2006-01-02"), "anon-empty", nil); err != nil {
		t.Fatalf("RecordWanted(empty): %v", err)
	}
	insights, err := pg.AdminInsights(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("AdminInsights: %v", err)
	}
	if got := insights.Flow.Week.NoMatches; got != 0 {
		t.Fatalf("an empty report counted as %d no-match questions", got)
	}
}

// The rows this panel exists to notice are the newest ones, and they are
// stamped by the database's clock while the window is cut with the caller's.
// Those are two clocks. A window closed at the caller's `now` therefore drops
// whatever was written while the database ran ahead — silently, as a zero, on
// the one card whose job is to say the line is alive.
//
// Observed here as a 2.16s skew between a container and its host; the same
// gap appears between an app server and a database that are not the same
// machine. A window that only opens has nothing to drop.
func TestIntegrationAdminFlowCountsRowsWrittenByAClockAheadOfTheCallers(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	ahead := flowNow.Add(5 * time.Second)

	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `
			INSERT INTO samples(sample_id, manifest, status, size_bytes, created_at, updated_at, quarantined)
			VALUES('sha256:flow-ahead', '{}'::jsonb, 'PUBLISHED', 1, $1, $1, false)`, ahead); err != nil {
			return err
		}
		if _, err := c.Exec(ctx, `
			INSERT INTO receipts(receipt_id, sample_id, peer_id, env_hash, receipt, contract_result, created_at)
			VALUES('flow-ahead', 'sha256:flow-ahead', 'peer', 'env', '{}'::jsonb, 'PASS', $1)`, ahead); err != nil {
			return err
		}
		_, err := c.Exec(ctx, `
			INSERT INTO search_misses(epoch, anon_id, dedup_key, created_at)
			VALUES('2026-08-24', 'anon-ahead', 'key-ahead', $1)`, ahead)
		return err
	}); err != nil {
		t.Fatalf("seed rows stamped ahead of the caller: %v", err)
	}

	insights, err := pg.AdminInsights(ctx, flowNow)
	if err != nil {
		t.Fatalf("AdminInsights: %v", err)
	}
	flow := insights.Flow
	if flow.Hour.AcceptedSamples != 1 {
		t.Errorf("accepted samples this hour = %d, want 1", flow.Hour.AcceptedSamples)
	}
	if got := flow.Hour.Verifications.Total(); got != 1 {
		t.Errorf("verifications this hour = %d, want 1", got)
	}
	if flow.Hour.NoMatches != 1 {
		t.Errorf("no-match questions this hour = %d, want 1", flow.Hour.NoMatches)
	}
}
