package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Nothing drains the upload queue outside community mode. Enqueueing an
// adoption report there piled up rows that would never be sent, while the
// tool answered the agent with "queued for anonymous upload" — to a user
// who had chosen the mode whose entire promise is that nothing is sent.
//
// The local record is the part that is real in that mode, and it stays:
// list_local_hits and get_local_stats are built on it.
func TestLocalOnlyRecordsAdoptionWithoutQueueingIt(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ident, err := identity.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}

	const sampleID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	pass := true
	for _, tc := range []struct {
		mode        string
		wantQueued  bool
		description string
	}{
		{config.ModeLocalOnly, false, "local-only sends nothing"},
		{config.ModeUninitialized, false, "no mode chosen means no permission given"},
		{config.ModeCommunity, true, "community is the mode that uploads"},
	} {
		before := queueLen(t, db)
		cfg := &config.Config{Mode: tc.mode}
		offerID, err := db.RecordSearchOffer(ctx, localdb.HitRow{SampleID: sampleID}, localdb.InterventionRow{
			SampleID: sampleID, ExactFailureMatched: true, VerifiedOffer: true,
		})
		if err != nil {
			t.Fatalf("record intervention: %v", err)
		}
		if _, err := reportAdoption(ctx, db, ident, cfg, offerID, sampleID, true, &pass); err != nil {
			t.Fatalf("%s: %v", tc.mode, err)
		}
		queued := queueLen(t, db) > before
		if queued != tc.wantQueued {
			t.Errorf("mode %q: queued=%v, want %v (%s)", tc.mode, queued, tc.wantQueued, tc.description)
		}
		// The correlated local intervention is kept in every mode.
		funnel, cerr := db.InterventionSummary(ctx)
		if cerr != nil {
			t.Fatal(cerr)
		}
		if funnel.Applied == 0 {
			t.Errorf("mode %q: the adoption was not recorded locally either", tc.mode)
		}
	}
}

func queueLen(t *testing.T, db *localdb.DB) int {
	t.Helper()
	items, err := db.QueuePending(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}
