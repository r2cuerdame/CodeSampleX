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

// FarmStatsStore reports the farm's state for the operations dashboard.
type FarmStatsStore interface {
	FarmWorkers(ctx context.Context, since, now time.Time) ([]FarmWorker, error)
	FarmHealthNow(ctx context.Context, now time.Time) (FarmHealth, error)
}
