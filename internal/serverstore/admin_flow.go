package serverstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Flow window lengths. An hour is short enough that a stopped lane shows up
// while the operator is still looking at the page; a day absorbs the gaps a
// small farm normally has; a week is the shape of the trend behind both.
const (
	adminFlowHour = time.Hour
	adminFlowDay  = 24 * time.Hour
	adminFlowWeek = 7 * 24 * time.Hour
)

// AdminFlowWindow is production measured over one bounded, recent window.
//
// The rest of the dashboard reports stock — what has accumulated. Stock cannot
// answer whether the line is running: a corpus of ten thousand verified samples
// looks identical whether the last verification finished a minute ago or in
// March. Every count here is scoped to a window and every window is named on
// screen, because a rate without its window is not a number an operator can act
// on.
type AdminFlowWindow struct {
	// Since is the start of the window in UTC; Length is its width.
	Since  time.Time
	Length time.Duration

	// NoMatches and Hits are deduplicated search outcomes, counted in the
	// same unit on both sides: one reporter's one question, once per UTC day.
	// Retries collapse; a report naming ten packages is still one question.
	NoMatches int64
	Hits      int64

	// Verifications is every completed verification receipt in the window,
	// not only the passing ones. A FAIL is a verifier that did its job, and
	// counting passes alone reports a stalled farm and a failing farm as the
	// same zero. The mix is what the drill-down is for.
	Verifications AdminVerificationCounts

	// AcceptedSamples is the samples uploaded in this window that the server
	// currently serves. HeldSamples is the ones uploaded in the same window
	// that are still quarantined — a draft awaiting its cross verification,
	// or one that was withdrawn. Producing and being usable are different
	// events, and merging them hides the case worth seeing: a producer that
	// is busy while nothing it makes reaches anyone.
	AcceptedSamples int64
	HeldSamples     int64
}

// SearchTotal is the denominator of the No-match rate: every search in the
// window whose outcome the server got to see.
func (w AdminFlowWindow) SearchTotal() int64 { return w.NoMatches + w.Hits }

// AdminFlow is the bounded aggregate behind the three operator questions this
// dashboard could not previously answer: is search still finding things, are
// verifiers still finishing work, and are samples still becoming usable.
//
// The Last* timestamps are what separate a normal zero from a broken one. An
// hour with no verifications and a last receipt twelve minutes old is a lull;
// the same zero with a last receipt three days old is a stopped lane, and
// without the timestamp the panel cannot tell the operator which one it is.
// They are absent, never substituted with the current time, when nothing was
// ever recorded.
type AdminFlow struct {
	Hour AdminFlowWindow
	Day  AdminFlowWindow
	Week AdminFlowWindow

	// PreviousHour is the hour before Hour, so the current hour can be read
	// as rising or falling rather than as a bare count.
	PreviousHour AdminFlowWindow

	LastVerification    time.Time
	HasLastVerification bool

	LastSample    time.Time
	HasLastSample bool

	LastSearchOutcome    time.Time
	HasLastSearchOutcome bool
}

// adminFlow reads every flow window in four bounded, index-backed aggregates.
// Each scan is limited to the last seven days; the three "last seen" values
// are unbounded but resolve through a descending index rather than a scan.
//
// Every window opens and never closes at `now`. The rows this panel exists to
// notice are the newest ones, and they are stamped by the database's clock
// while the window is cut with the caller's -- two clocks. A window closed at
// the caller's `now` drops whatever was written while the database ran ahead,
// silently, as a zero, on the one card whose job is to say the line is alive.
// The only closed boundary here is the previous hour's, which ends at a past
// instant no clock is racing.
func adminFlow(ctx context.Context, conn *pgx.Conn, now time.Time) (AdminFlow, error) {
	now = now.UTC()
	hourStart := now.Add(-adminFlowHour)
	dayStart := now.Add(-adminFlowDay)
	weekStart := now.Add(-adminFlowWeek)
	previousHourStart := now.Add(-2 * adminFlowHour)

	flow := AdminFlow{
		Hour:         AdminFlowWindow{Since: hourStart, Length: adminFlowHour},
		Day:          AdminFlowWindow{Since: dayStart, Length: adminFlowDay},
		Week:         AdminFlowWindow{Since: weekStart, Length: adminFlowWeek},
		PreviousHour: AdminFlowWindow{Since: previousHourStart, Length: adminFlowHour},
	}

	// Hits and misses share one scan so the two halves of the rate can never
	// be read from different instants.
	rows, err := conn.Query(ctx, `
		SELECT source,
		       COUNT(*) FILTER (WHERE created_at >= $1),
		       COUNT(*) FILTER (WHERE created_at >= $2),
		       COUNT(*),
		       COUNT(*) FILTER (WHERE created_at >= $4 AND created_at < $1)
		FROM (
			SELECT 'hit'::text AS source, created_at FROM search_hits WHERE created_at >= $3
			UNION ALL
			SELECT 'miss'::text, created_at FROM search_misses WHERE created_at >= $3
		) outcomes
		GROUP BY source`, hourStart, dayStart, weekStart, previousHourStart)
	if err != nil {
		return flow, err
	}
	for rows.Next() {
		var source string
		var hour, day, week, previous int64
		if err := rows.Scan(&source, &hour, &day, &week, &previous); err != nil {
			rows.Close()
			return flow, err
		}
		switch source {
		case "hit":
			flow.Hour.Hits, flow.Day.Hits = hour, day
			flow.Week.Hits, flow.PreviousHour.Hits = week, previous
		case "miss":
			flow.Hour.NoMatches, flow.Day.NoMatches = hour, day
			flow.Week.NoMatches, flow.PreviousHour.NoMatches = week, previous
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return flow, err
	}
	rows.Close()

	rows, err = conn.Query(ctx, `
		SELECT bucket,
		       COUNT(*) FILTER (WHERE created_at >= $1),
		       COUNT(*) FILTER (WHERE created_at >= $2),
		       COUNT(*),
		       COUNT(*) FILTER (WHERE created_at >= $4 AND created_at < $1)
		FROM (
			SELECT created_at,
			       CASE UPPER(COALESCE(contract_result, ''))
					WHEN 'PASS' THEN 'PASS'
					WHEN 'FAIL' THEN 'FAIL'
					WHEN 'SKIPPED' THEN 'SKIPPED'
					ELSE 'UNCLASSIFIED'
			       END AS bucket
			FROM receipts
			WHERE created_at >= $3
		) recent
		GROUP BY bucket`, hourStart, dayStart, weekStart, previousHourStart)
	if err != nil {
		return flow, err
	}
	for rows.Next() {
		var bucket string
		var hour, day, week, previous int64
		if err := rows.Scan(&bucket, &hour, &day, &week, &previous); err != nil {
			rows.Close()
			return flow, err
		}
		for _, target := range []struct {
			counts *AdminVerificationCounts
			value  int64
		}{
			{&flow.Hour.Verifications, hour},
			{&flow.Day.Verifications, day},
			{&flow.Week.Verifications, week},
			{&flow.PreviousHour.Verifications, previous},
		} {
			switch bucket {
			case "PASS":
				target.counts.Pass += target.value
			case "FAIL":
				target.counts.Fail += target.value
			case "SKIPPED":
				target.counts.Skipped += target.value
			default:
				target.counts.Unclassified += target.value
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return flow, err
	}
	rows.Close()

	if err := conn.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE created_at >= $1 AND NOT quarantined),
		       COUNT(*) FILTER (WHERE created_at >= $1 AND quarantined),
		       COUNT(*) FILTER (WHERE created_at >= $2 AND NOT quarantined),
		       COUNT(*) FILTER (WHERE created_at >= $2 AND quarantined),
		       COUNT(*) FILTER (WHERE NOT quarantined),
		       COUNT(*) FILTER (WHERE quarantined),
		       COUNT(*) FILTER (WHERE created_at >= $4 AND created_at < $1 AND NOT quarantined),
		       COUNT(*) FILTER (WHERE created_at >= $4 AND created_at < $1 AND quarantined)
		FROM samples
		WHERE created_at >= $3`,
		hourStart, dayStart, weekStart, previousHourStart).Scan(
		&flow.Hour.AcceptedSamples, &flow.Hour.HeldSamples,
		&flow.Day.AcceptedSamples, &flow.Day.HeldSamples,
		&flow.Week.AcceptedSamples, &flow.Week.HeldSamples,
		&flow.PreviousHour.AcceptedSamples, &flow.PreviousHour.HeldSamples,
	); err != nil {
		return flow, err
	}

	// GREATEST ignores NULL arguments in PostgreSQL, so a server that has
	// recorded hits but no misses still reports its last search outcome, and
	// one that has recorded neither reports NULL rather than a zero time.
	var lastVerification, lastSample, lastSearch *time.Time
	if err := conn.QueryRow(ctx, `
		SELECT (SELECT MAX(created_at) FROM receipts),
		       (SELECT MAX(created_at) FROM samples WHERE NOT quarantined),
		       GREATEST((SELECT MAX(created_at) FROM search_hits),
		                (SELECT MAX(created_at) FROM search_misses))`).Scan(
		&lastVerification, &lastSample, &lastSearch); err != nil {
		return flow, err
	}
	if lastVerification != nil {
		flow.LastVerification, flow.HasLastVerification = lastVerification.UTC(), true
	}
	if lastSample != nil {
		flow.LastSample, flow.HasLastSample = lastSample.UTC(), true
	}
	if lastSearch != nil {
		flow.LastSearchOutcome, flow.HasLastSearchOutcome = lastSearch.UTC(), true
	}
	return flow, nil
}
