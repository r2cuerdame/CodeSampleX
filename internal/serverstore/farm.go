package serverstore

import (
	"context"
	"time"
)

// FarmWorker is one live authoring session and what it has actually produced.
//
// The fields are the ones that would have caught the failures this dashboard
// exists for: a session issued but never refreshed (a worker that failed to
// start), and a worker whose draft count stops moving.
type FarmWorker struct {
	Label         string
	ComputerName  string // reported by the worker itself; "" until it refreshes
	IssuedAt      time.Time
	LastRefreshAt time.Time // zero = never came alive
	IdleExpiresAt time.Time
	Drafts        int    // submitted inside the window
	Published     int    // of those, now public
	Holding       string // the coordinate it currently claims; "" if none
}

// FarmHealth is what the network looks like as a whole.
//
// DuplicateCoords is the one that matters most: it sat at 37% for a day
// without appearing anywhere an operator could see it.
type FarmHealth struct {
	PublicSamples   int
	DuplicateCoords int // coordinates carrying more than one public sample
	StaleClaims     int // open claims whose session is no longer live
	// QuarantinedByReason is why things were withdrawn, counted by reason.
	//
	// Every withdrawal already recorded one; none of it reached the operator,
	// so seeing why anything was pulled meant opening a database. A bare
	// count cannot be acted on -- "983 quarantined" is alarming and "983
	// duplicate coordinates, superseded" is a finished piece of work. The
	// empty key is deliberate: a withdrawal nobody explained is the row most
	// worth surfacing.
	QuarantinedByReason map[string]int
	ReceiptsByOS        map[string]int // PASS receipts per operating system
	// WithheldCoordinates is how many public coordinates the authoring queue
	// is currently refusing to offer, and WithheldByReason is why.
	//
	// They are read from exactly the state the picker consults, because an
	// operator reading "0 withheld" while the fleet is being refused work is
	// the failure the attempt ledger exists to make visible.
	WithheldCoordinates int
	WithheldByReason    map[string]int
}

// FarmAxisCoverage is one (os, ecosystem) cell of the compatibility map:
// how many public packages the network has seen used there, against how many
// it has actually proven there.
//
// The gap is the point. Observations come from developer machines and
// verifications come from containers, so an ecosystem can be heavily used on
// one platform and proven only on another — which is precisely what this
// network exists to notice about itself.
type FarmAxisCoverage struct {
	OS        string
	Ecosystem string
	// Observed is distinct public purls that developer machines reported
	// using on this OS. Relayed, never graded.
	Observed int
	// Measured is distinct public purls the fleet actually RAN on this OS,
	// pass or fail. A failed run is a measurement of this platform, and
	// counting only passes made "we ran it here and it broke" read the same
	// as "we never came here" -- the exact collapse this panel exists to
	// prevent.
	Measured int
	// Proven is the subset of Measured whose run reached a contract PASS.
	Proven int
	// ObservedProven is |Observed AND Proven|: of what we see used here, how
	// much have we actually proven here. It is a separate field rather than
	// a re-reading of Proven because that ambiguity is precisely what made
	// the Fake and the PG store disagree -- the Fake intersected, the SQL
	// did not, and the doc comment claimed the intersection.
	ObservedProven int
}

// FarmBacklog is the coverage gap the queue is working through, and how fast
// it is moving through it.
//
// The farm panel could say how much had been proven and nothing at all about
// how much was left, so "the fleet is busy" and "the fleet is nearly done"
// looked identical -- and a queue that generates its own work can be busy
// forever. These are the two stocks and the two flows.
//
// A `-` cell and an unopened dependency are counted apart on purpose. They are
// different absences: one is a package the network watches people use and has
// never proven, the other is a release nobody has reported at all and that
// only a resolved lockfile even names. Pooling them would hide which of the
// two the fleet is actually failing to drain.
type FarmBacklog struct {
	// CoverageHoles is how many PUBLIC coordinates carry evidence and no
	// passing sample: the `-` cells, counted.
	CoverageHoles int
	// Dependencies is how many coordinates are reachable only through a
	// resolved dependency edge and still carry no evidence and no proof.
	//
	// It is the WHOLE backlog, not the bounded slice one scheduling pass
	// offers. A backlog reported at its own cap reads as finished work.
	Dependencies int
	// ClaimedByKind is work handed out inside the window, by queue source.
	// This is the generation rate: what the scheduler actually produced,
	// rather than what it could have.
	ClaimedByKind map[string]int
	// FirstProven is how many coordinates earned their first passing receipt
	// inside the window -- the resolution rate.
	//
	// First, not any: re-proving a coordinate on another platform is real
	// work and real evidence, but it does not take anything off the backlog
	// above, and a rate that counted it would never square with a stock that
	// does not.
	FirstProven int
}

// FarmStatsStore reports the farm's state for the operations dashboard.
type FarmStatsStore interface {
	FarmCoverage(ctx context.Context) ([]FarmAxisCoverage, error)
	FarmWorkers(ctx context.Context, since, now time.Time) ([]FarmWorker, error)
	FarmHealthNow(ctx context.Context, now time.Time) (FarmHealth, error)
	// FarmBacklogNow reports the coverage gap now and the flow through it
	// since. The window is the caller's so the panel can keep one window for
	// every rate it shows.
	FarmBacklogNow(ctx context.Context, since, now time.Time) (FarmBacklog, error)
}
