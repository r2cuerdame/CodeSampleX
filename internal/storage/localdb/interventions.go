package localdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var (
	// ErrOfferIDRequired makes legacy and pre-upgrade adoption calls neutral.
	// Without the opaque capability there is no way to identify which search
	// result was used, so the caller must re-search instead of receiving
	// failure-avoidance credit.
	ErrOfferIDRequired = errors.New("offerId required; re-run search_known_solution and report the returned local offerId")
	// ErrNoEligibleIntervention means the exact local offer capability and
	// sample ID do not identify an unreported search result.
	ErrNoEligibleIntervention = errors.New("no eligible local CodeSampleX offer for this offerId and sampleId; re-run search_known_solution")
	// ErrInterventionHitMismatch indicates local correlation data is damaged.
	// The transaction is rolled back so neither side becomes half-reported.
	ErrInterventionHitMismatch = errors.New("local CodeSampleX offer no longer matches its exact hit")
)

// InterventionRow is the complete local-only journey record. Its shape is
// intentionally narrower than HitRow: no query, fingerprint, package,
// environment, path, user or peer identity is stored here or uploaded.
type InterventionRow struct {
	TS                  time.Time
	SampleID            string
	ExactFailureMatched bool
	VerifiedOffer       bool
	Applied             sql.NullBool
	BuildPass           sql.NullBool
}

// InterventionStats is the honest successive failure-detour funnel. Every
// stage after the first is counted only when all preceding stages are true.
type InterventionStats struct {
	ExactFailureMatches     int
	VerifiedDetoursOffered  int
	Applied                 int
	PostHitPass             int
	PostHitFail             int
	PostHitUnknown          int
	ReportedFailuresAvoided int
}

// InterventionOutcome describes the correlated row after an adoption
// report. FailureAvoided is true only when all four measured stages exist.
type InterventionOutcome struct {
	ExactFailureMatched bool
	VerifiedOffer       bool
	Applied             bool
	BuildPass           sql.NullBool
	UploadQueued        bool
}

// ReportedFailureAvoided derives the label from the four measured stages;
// callers cannot set or trust a separate flattering boolean.
func (o InterventionOutcome) ReportedFailureAvoided() bool {
	return o.ExactFailureMatched && o.VerifiedOffer && o.Applied &&
		o.BuildPass.Valid && o.BuildPass.Bool
}

// RecordSearchOffer writes one hit and its local-only intervention in the
// same transaction, returning a random 128-bit capability. The token is
// protected by a UNIQUE index and is never put in an upload payload.
//
// It is the single-candidate form of RecordSearchOffers.
func (d *DB) RecordSearchOffer(ctx context.Context, hit HitRow, intervention InterventionRow) (string, error) {
	return d.RecordSearchOffers(ctx, hit, []InterventionRow{intervention})
}

// RecordSearchOffers writes one hit for the search and one intervention row
// per candidate the search listed, all under the same offer capability and
// the same hit_id, in one transaction.
//
// search_known_solution returns a ranked list and the agent chooses; the
// offer used to record Results[0] only, so applying the second candidate
// reported ErrNoEligibleIntervention and re-searching could never fix it
// (#344). The hit row is the search's history entry and names the top
// candidate until a report says which one was actually applied; there is
// exactly one, so one search that listed three answers is still one hit.
// Candidates listed twice are recorded once.
func (d *DB) RecordSearchOffers(ctx context.Context, hit HitRow, candidates []InterventionRow) (string, error) {
	if len(candidates) == 0 {
		return "", errors.New("search offer needs at least one candidate")
	}
	if hit.SampleID == "" || hit.SampleID != candidates[0].SampleID {
		return "", errors.New("search hit and first candidate must name the same nonempty sampleId")
	}
	seen := make(map[string]bool, len(candidates))
	distinct := make([]InterventionRow, 0, len(candidates))
	for _, c := range candidates {
		if c.SampleID == "" {
			return "", errors.New("every search candidate must name a nonempty sampleId")
		}
		if seen[c.SampleID] {
			continue
		}
		seen[c.SampleID] = true
		distinct = append(distinct, c)
	}
	now := hit.TS
	if now.IsZero() {
		now = candidates[0].TS
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// Retrying a cryptographic collision is mostly theoretical, but the
	// database constraint remains the authority and a retry keeps the API
	// correct even under a deterministic/fault-injection random source.
	for attempt := 0; attempt < 4; attempt++ {
		offerID, err := randomOfferID()
		if err != nil {
			return "", err
		}
		tx, err := d.sql.BeginTx(ctx, nil)
		if err != nil {
			return "", err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO hits(ts, query, grade, sample_id, adopted, post_build_pass)
			VALUES(?, ?, ?, ?, 0, NULL)`,
			timeArg(now), hit.Query, string(hit.Grade), hit.SampleID)
		if err != nil {
			tx.Rollback()
			return "", err
		}
		hitID, err := res.LastInsertId()
		if err != nil {
			tx.Rollback()
			return "", err
		}
		// Candidates are distinct and the hit row is new, so the only row
		// the UNIQUE (offer_id, sample_id) index can refuse here is one
		// whose offer_id already belongs to an earlier search: a collision,
		// which retries with a fresh token.
		collided := false
		for _, c := range distinct {
			res, err = tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO interventions(
					ts, offer_id, hit_id, sample_id,
					exact_failure_matched, verified_offer, applied, build_pass)
				VALUES(?, ?, ?, ?, ?, ?, NULL, NULL)`,
				timeArg(now), offerID, hitID, c.SampleID,
				boolInt(c.ExactFailureMatched), boolInt(c.VerifiedOffer))
			if err != nil {
				tx.Rollback()
				return "", err
			}
			inserted, err := res.RowsAffected()
			if err != nil {
				tx.Rollback()
				return "", err
			}
			if inserted != 1 {
				collided = true
				break
			}
		}
		if collided {
			tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		// S6, and the far end of the only duration worth having
		// (docs/activation-funnel.md §5). Stamped from the row's own ts, not
		// from wall-clock now, so a replayed or backdated hit does not claim a
		// first answer at the moment it happened to be written. Best effort:
		// the offer is already committed and the caller is owed its token, so
		// a failed stamp leaves the stage unmeasured rather than failing a
		// search that worked.
		_ = d.StampFirst(ctx, StatFirstHitAt, now)
		return offerID, nil
	}
	return "", errors.New("could not allocate a unique local offerId")
}

// OfferCandidates is one intervention row per returned candidate, each with
// its own exact-match and verification flags, so the funnel credits the
// candidate that was applied rather than the one that happened to rank first.
func OfferCandidates(now time.Time, results []domain.SearchResult) []InterventionRow {
	rows := make([]InterventionRow, 0, len(results))
	for _, r := range results {
		rows = append(rows, InterventionRow{
			TS:                  now,
			SampleID:            r.SampleID,
			ExactFailureMatched: r.ExactFailureMatched,
			VerifiedOffer:       r.VerifiedOffer(),
		})
	}
	return rows
}

func randomOfferID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate local offerId: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// CorrelateInterventionAdoption updates exactly the unreported offer named
// by offerID and sampleID, then its exact hit_id, in one transaction. It
// never guesses from recency and never creates an unsolicited row.
func (d *DB) CorrelateInterventionAdoption(ctx context.Context, offerID, sampleID string,
	applied bool, buildPass sql.NullBool, adoptionPayload string) (InterventionOutcome, error) {
	var out InterventionOutcome
	if offerID == "" {
		return out, ErrOfferIDRequired
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	var hitID int64
	var exact, offer int
	err = tx.QueryRowContext(ctx, `
		UPDATE interventions SET applied = ?, build_pass = ?
		WHERE offer_id = ? AND sample_id = ? AND applied IS NULL
		RETURNING hit_id, exact_failure_matched, verified_offer`,
		boolInt(applied), buildPass, offerID, sampleID).Scan(&hitID, &exact, &offer)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNoEligibleIntervention
	}
	if err != nil {
		return out, err
	}

	// Hits remain the backwards-compatible local history surface. Requiring
	// one exact hit_id prevents two searches for the same sample from stealing
	// each other's adoption result.
	//
	// One-use is enforced per (offer, candidate) above; the hit row is the
	// search's single history entry and follows the reports on its
	// candidates. An applied report makes the search adopted and names the
	// sample that was actually used — the row was written with the top
	// candidate, which is not necessarily the one the agent chose. A
	// rejection marks the search only while nothing has been reported on
	// it: "I did not use this one" about a sibling never downgrades an
	// adoption, and never overwrites which sample was adopted.
	var res sql.Result
	if applied {
		res, err = tx.ExecContext(ctx, `
			UPDATE hits SET adopted = 1, post_build_pass = ?, sample_id = ?
			WHERE id = ?`, buildPass, sampleID, hitID)
	} else {
		res, err = tx.ExecContext(ctx, `
			UPDATE hits SET
				adopted = CASE WHEN adopted = 0 THEN -1 ELSE adopted END,
				post_build_pass = CASE WHEN adopted = 0 THEN ? ELSE post_build_pass END
			WHERE id = ?`, buildPass, hitID)
	}
	if err != nil {
		return out, err
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return out, err
	}
	if updated != 1 {
		return out, ErrInterventionHitMismatch
	}
	// In community mode the existing upload queue is the transactional
	// outbox. Enqueue before commit so a queue failure rolls back both the
	// intervention and hit updates; the one-use offer token remains retryable.
	// An empty payload is the explicit local-only path and writes no outbox.
	if adoptionPayload != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO upload_queue(kind, payload, created_at, attempts, last_error)
			VALUES('adoption', ?, ?, 0, '')`, adoptionPayload, nowText()); err != nil {
			return out, err
		}
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}

	// S7. Only an APPLIED report is an adoption: an explicit "I did not use
	// this" is a completed report that the store already writes as -1 and
	// every surface already shows as adopted=false, so stamping it would make
	// the readiness panel say a sample was adopted when the user said it was
	// not.
	if applied {
		_ = d.StampFirst(ctx, StatFirstAdoptionAt, time.Now().UTC())
	}

	out = InterventionOutcome{
		ExactFailureMatched: exact != 0,
		VerifiedOffer:       offer != 0,
		Applied:             applied,
		BuildPass:           buildPass,
		UploadQueued:        adoptionPayload != "",
	}
	return out, nil
}

// InterventionSummary returns the local four-stage funnel. PASS and FAIL
// are actual post-hit reports; unknown remains visible and is never folded
// into either result.
//
// The funnel counts searches, not candidates. A search that listed three
// exact-match candidates offered one exact match, not three, and the rows
// under one offer are folded first: a search applied any candidate, passed
// when any applied candidate's build passed, failed when one was measured
// and none passed, and is unknown only when nothing applied was measured.
// So pass + fail + unknown is still the applied count.
func (d *DB) InterventionSummary(ctx context.Context) (InterventionStats, error) {
	var s InterventionStats
	err := d.sql.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(exact), 0),
			COALESCE(SUM(verified), 0),
			COALESCE(SUM(applied), 0),
			COALESCE(SUM(pass), 0),
			COALESCE(SUM(CASE WHEN pass = 0 THEN fail ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pass = 0 AND fail = 0 THEN unknown ELSE 0 END), 0)
		FROM (
			SELECT
				MAX(CASE WHEN exact_failure_matched = 1 THEN 1 ELSE 0 END) AS exact,
				MAX(CASE WHEN exact_failure_matched = 1 AND verified_offer = 1 THEN 1 ELSE 0 END) AS verified,
				MAX(CASE WHEN exact_failure_matched = 1 AND verified_offer = 1 AND applied = 1 THEN 1 ELSE 0 END) AS applied,
				MAX(CASE WHEN exact_failure_matched = 1 AND verified_offer = 1 AND applied = 1 AND build_pass = 1 THEN 1 ELSE 0 END) AS pass,
				MAX(CASE WHEN exact_failure_matched = 1 AND verified_offer = 1 AND applied = 1 AND build_pass = 0 THEN 1 ELSE 0 END) AS fail,
				MAX(CASE WHEN exact_failure_matched = 1 AND verified_offer = 1 AND applied = 1 AND build_pass IS NULL THEN 1 ELSE 0 END) AS unknown
			FROM interventions
			WHERE offer_id IS NOT NULL AND hit_id IS NOT NULL
			GROUP BY offer_id)`).Scan(
		&s.ExactFailureMatches,
		&s.VerifiedDetoursOffered,
		&s.Applied,
		&s.PostHitPass,
		&s.PostHitFail,
		&s.PostHitUnknown,
	)
	if err != nil {
		return s, err
	}
	s.ReportedFailuresAvoided = s.PostHitPass
	return s, nil
}
