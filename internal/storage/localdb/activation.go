package localdb

import (
	"context"
	"database/sql"
	"time"
)

// The local activation ledger (docs/activation-funnel.md §7). S1, S2 and S4
// of the funnel had no signal at all — locally or on the wire — and S3 had a
// fact with no time on it, which is why nothing in this product could say
// whether an install had ever worked, only how many records it had
// accumulated. These seven stamps are that signal, and they are §2.1 local
// status: they never enter an upload payload in any mode, because the top of
// the funnel happens before `csx init` asks the mode question and
// transmitting it would be collecting before consent.
//
// They live in meta under the existing "stat:" namespace so they inherit the
// store, the migration and the local-only guarantee already in place.
const (
	StatFirstRunAt      = "firstRunAt"      // S1: cli.Main entered, before argv is even inspected
	StatInitAt          = "initAt"          // S2: `csx init` persisted a mode
	StatFirstSyncAt     = "firstSyncAt"     // S3: a sync warmed at least one shard key
	StatMCPFirstReadyAt = "mcpFirstReadyAt" // S4: an MCP session completed the protocol handshake
	StatMCPLastReadyAt  = "mcpLastReadyAt"  // S4, most recent — the only stamp that moves
	StatFirstHitAt      = "firstHitAt"      // S6: the hit writer wrote a row
	StatFirstAdoptionAt = "firstAdoptionAt" // S7: an adoption report said applied
)

// FirstStampKeys is every write-once key. A later run must not move any of
// them: the S2→S6 duration in §5 is only meaningful measured from the first
// occurrence, and a stamp that advanced would report "time since the last
// search" under the name "time to first useful answer".
var FirstStampKeys = []string{
	StatFirstRunAt,
	StatInitAt,
	StatFirstSyncAt,
	StatMCPFirstReadyAt,
	StatFirstHitAt,
	StatFirstAdoptionAt,
}

// Activation is the ledger read back. A zero time means the stage has not
// been reached, which is not the same as reaching it at the zero instant —
// §6: an unmeasured thing renders as an em dash, never as a number.
type Activation struct {
	FirstRunAt      time.Time
	InitAt          time.Time
	FirstSyncAt     time.Time
	MCPFirstReadyAt time.Time
	MCPLastReadyAt  time.Time
	FirstHitAt      time.Time
	FirstAdoptionAt time.Time
	// Unmeasured names the stages this install reached before anything was
	// recording them, by stamp key.
	//
	// The sentinel exists so that "reached, but before the ledger did" and
	// "never reached" are different states — and parseStamp turns both into
	// the zero time, so without this the difference dies here and every
	// reader downstream sees a fresh install. It is what stopped the panel
	// telling a machine that had been up for days to run csx init.
	Unmeasured map[string]bool
}

// TimeToFirstAnswer is the §5 duration: init (the consent choice, S2) to the
// first hit (S6). ok is false unless both endpoints exist, and it stays local
// — uploading it would be uploading a cross-day fact about one install, which
// is exactly the linkability the daily anonId rotation prevents.
func (a Activation) TimeToFirstAnswer() (time.Duration, bool) {
	if a.InitAt.IsZero() || a.FirstHitAt.IsZero() || a.FirstHitAt.Before(a.InitAt) {
		return 0, false
	}
	return a.FirstHitAt.Sub(a.InitAt), true
}

// StampFirst records t under key only if the key has never been written.
// DO NOTHING rather than a read-then-write because the daemon, an MCP server
// and the CLI share this database by design, and two of them reaching a stage
// in the same second must not race the earlier stamp away.
func (d *DB) StampFirst(ctx context.Context, key string, t time.Time) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO NOTHING`,
		statPrefix+key, stampText(t))
	return err
}

// Stamp records t under key, overwriting. Only mcpLastReadyAt uses it: the
// readiness panel has to distinguish "the MCP path worked once, in June" from
// "it worked four minutes ago".
func (d *DB) Stamp(ctx context.Context, key string, t time.Time) error {
	return d.SetStat(ctx, key, stampText(t))
}

// ActivationLedger reads every stamp in one pass.
func (d *DB) ActivationLedger(ctx context.Context) (Activation, error) {
	var a Activation
	all, err := d.AllStats(ctx)
	if err != nil {
		return a, err
	}
	for _, k := range append([]string{StatMCPLastReadyAt}, FirstStampKeys...) {
		if all[k] == activationUnmeasured {
			if a.Unmeasured == nil {
				a.Unmeasured = map[string]bool{}
			}
			a.Unmeasured[k] = true
		}
	}
	a.FirstRunAt = parseStamp(all[StatFirstRunAt])
	a.InitAt = parseStamp(all[StatInitAt])
	a.FirstSyncAt = parseStamp(all[StatFirstSyncAt])
	a.MCPFirstReadyAt = parseStamp(all[StatMCPFirstReadyAt])
	a.MCPLastReadyAt = parseStamp(all[StatMCPLastReadyAt])
	a.FirstHitAt = parseStamp(all[StatFirstHitAt])
	a.FirstAdoptionAt = parseStamp(all[StatFirstAdoptionAt])
	return a, nil
}

func stampText(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(time.RFC3339)
}

// parseStamp turns an absent or unreadable value into the zero time. A stamp
// nobody can parse is an unmeasured stage, and §6 forbids dressing that up as
// a measurement.
func parseStamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// activationUnmeasured marks a stage this install reached before anything was
// recording it. parseStamp cannot read it, so the ledger returns the zero
// time and §6 renders an em dash — while StampFirst's DO NOTHING keeps a
// later run from writing today's date over the gap.
//
// A value rather than an absent key, because absence is what a fresh install
// looks like and the two must not read the same.
const activationUnmeasured = "unmeasured"

// BackfillActivation derives what the store can already answer and refuses to
// invent the rest.
//
// The stamps are write-once and were only ever written going forward, so on
// every install that already existed they were all empty — and the next run
// stamped firstRunAt as TODAY, labelled the 4,735th hit "first answer" and
// the 110th adoption "first adoption". Measured on one real store: 4,734 hits
// with the oldest at 2026-08-15, 109 adoptions, and a completely empty
// funnel. A panel that reports a fortnight-old install's first day as today
// is worse than one that says it does not know.
//
// hits and interventions carry their own timestamps, so S6 and S7 are facts
// the store holds and are read from it. S1, S2, S3 and S4 are not recorded
// anywhere — nothing wrote them before these stamps existed — so on a store
// that predates them they are marked unmeasured rather than guessed.
func (d *DB) BackfillActivation(ctx context.Context, now time.Time) error {
	all, err := d.AllStats(ctx)
	if err != nil {
		return err
	}

	derived := []struct {
		key   string
		query string
	}{
		{StatFirstHitAt, `SELECT MIN(ts) FROM hits WHERE ts IS NOT NULL AND ts <> ''`},
		{StatFirstAdoptionAt, `SELECT MIN(ts) FROM interventions WHERE applied = 1 AND ts IS NOT NULL AND ts <> ''`},
	}
	earliest := time.Time{}
	for _, s := range derived {
		var text sql.NullString
		if err := d.sql.QueryRowContext(ctx, s.query).Scan(&text); err != nil {
			return err
		}
		if !text.Valid || text.String == "" {
			continue
		}
		when := parseStamp(text.String)
		if when.IsZero() {
			continue
		}
		if earliest.IsZero() || when.Before(earliest) {
			earliest = when
		}
		if all[s.key] == "" {
			if err := d.StampFirst(ctx, s.key, when); err != nil {
				return err
			}
		}
	}

	// Nothing older than the stamps themselves: this really is a new install
	// and the ordinary write-once path should record its stages as they
	// happen.
	if earliest.IsZero() || !earliest.Before(now) {
		return nil
	}
	for _, key := range []string{StatFirstRunAt, StatInitAt, StatFirstSyncAt, StatMCPFirstReadyAt} {
		if all[key] != "" {
			continue
		}
		if _, err := d.sql.ExecContext(ctx, `
			INSERT INTO meta(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO NOTHING`,
			statPrefix+key, activationUnmeasured); err != nil {
			return err
		}
	}
	return nil
}
