package localdb

import (
	"context"
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
