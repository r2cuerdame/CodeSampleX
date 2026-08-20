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
	DuplicateCoords int            // coordinates carrying more than one public sample
	StaleClaims     int            // open claims whose session is no longer live
	ReceiptsByOS    map[string]int // PASS receipts per operating system
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

// FarmStatsStore reports the farm's state for the operations dashboard.
type FarmStatsStore interface {
	FarmCoverage(ctx context.Context) ([]FarmAxisCoverage, error)
	FarmWorkers(ctx context.Context, since, now time.Time) ([]FarmWorker, error)
	FarmHealthNow(ctx context.Context, now time.Time) (FarmHealth, error)
}
