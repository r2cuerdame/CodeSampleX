package serverstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func runPresenceStoreContract(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()

	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	unixDay := now.Unix() / 86400
	epoch1d := now.Format("2006-01-02")
	epoch7d := strconv.FormatInt(unixDay/7, 10)
	epoch30d := strconv.FormatInt(unixDay/30, 10)

	// Initially zero active installations
	counts0, err := store.NetworkCounts(ctx, now)
	if err != nil {
		t.Fatalf("initial NetworkCounts: %v", err)
	}
	if counts0.ActiveInstallations1d != 0 || counts0.ActiveInstallations7d != 0 || counts0.ActiveInstallations30d != 0 {
		t.Fatalf("initial counts not zero: %+v", counts0)
	}

	// 1. Record first public presence ("ordinary")
	p1 := domain.PresencePayload{
		SchemaVersion: 1,
		ClientClass:   "ordinary",
		ClientVersion: "v0.1.0",
		Epoch1d:       epoch1d,
		Token1d:       strings.Repeat("1", 32),
		Epoch7d:       epoch7d,
		Token7d:       strings.Repeat("2", 32),
		Epoch30d:      epoch30d,
		Token30d:      strings.Repeat("3", 32),
	}
	if err := store.RecordPresence(ctx, p1, now); err != nil {
		t.Fatalf("record p1: %v", err)
	}

	counts1, err := store.NetworkCounts(ctx, now)
	if err != nil {
		t.Fatalf("counts after p1: %v", err)
	}
	if counts1.ActiveInstallations1d != 1 || counts1.ActiveInstallations7d != 1 || counts1.ActiveInstallations30d != 1 {
		t.Fatalf("counts after p1 = %+v, want 1/1/1", counts1)
	}

	// 2. Idempotent repeat of p1 (e.g. updated version later the same day)
	p1Update := p1
	p1Update.ClientVersion = "v0.1.1"
	if err := store.RecordPresence(ctx, p1Update, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("record p1 repeat: %v", err)
	}
	counts1Repeat, err := store.NetworkCounts(ctx, now)
	if err != nil {
		t.Fatalf("counts after p1 repeat: %v", err)
	}
	if counts1Repeat.ActiveInstallations1d != 1 || counts1Repeat.ActiveInstallations7d != 1 || counts1Repeat.ActiveInstallations30d != 1 {
		t.Fatalf("counts after repeat = %+v, want 1/1/1", counts1Repeat)
	}

	// 3. Second public presence ("external")
	p2 := domain.PresencePayload{
		SchemaVersion: 1,
		ClientClass:   "external",
		ClientVersion: "v0.2.0",
		Epoch1d:       epoch1d,
		Token1d:       strings.Repeat("4", 32),
		Epoch7d:       epoch7d,
		Token7d:       strings.Repeat("5", 32),
		Epoch30d:      epoch30d,
		Token30d:      strings.Repeat("6", 32),
	}
	if err := store.RecordPresence(ctx, p2, now); err != nil {
		t.Fatalf("record p2: %v", err)
	}
	counts2, err := store.NetworkCounts(ctx, now)
	if err != nil {
		t.Fatalf("counts after p2: %v", err)
	}
	if counts2.ActiveInstallations1d != 2 || counts2.ActiveInstallations7d != 2 || counts2.ActiveInstallations30d != 2 {
		t.Fatalf("counts after p2 = %+v, want 2/2/2", counts2)
	}

	// 4. Internal / Farm / CI / Verifier / Operator presence
	// These must be accepted and stored, but EXCLUDED from public counts
	internalClasses := []string{"farm", "ci", "verifier", "operator", "internal"}
	for i, cls := range internalClasses {
		tokHex := fmt.Sprintf("%x", i+10)
		internalP := domain.PresencePayload{
			SchemaVersion: 1,
			ClientClass:   cls,
			ClientVersion: "dev",
			Epoch1d:       epoch1d,
			Token1d:       strings.Repeat(tokHex[:1], 32),
			Epoch7d:       epoch7d,
			Token7d:       strings.Repeat(tokHex[:1], 32),
			Epoch30d:      epoch30d,
			Token30d:      strings.Repeat(tokHex[:1], 32),
		}
		if err := store.RecordPresence(ctx, internalP, now); err != nil {
			t.Fatalf("record internal class %q: %v", cls, err)
		}
	}

	countsAfterInternal, err := store.NetworkCounts(ctx, now)
	if err != nil {
		t.Fatalf("counts after internal: %v", err)
	}
	if countsAfterInternal.ActiveInstallations1d != 2 || countsAfterInternal.ActiveInstallations7d != 2 || countsAfterInternal.ActiveInstallations30d != 2 {
		t.Fatalf("internal nodes leaked into counts: %+v, want 2/2/2", countsAfterInternal)
	}

	// 5. Aligned epoch counting semantics
	// Next day (2026-09-13): 1d epoch has rotated; 7d and 30d epochs are still the same
	nextDay := now.Add(24 * time.Hour)
	countsNextDay, err := store.NetworkCounts(ctx, nextDay)
	if err != nil {
		t.Fatalf("counts next day: %v", err)
	}
	if countsNextDay.ActiveInstallations1d != 0 {
		t.Errorf("ActiveInstallations1d on next day = %d, want 0", countsNextDay.ActiveInstallations1d)
	}
	if countsNextDay.ActiveInstallations7d != 2 {
		t.Errorf("ActiveInstallations7d within 7d block = %d, want 2", countsNextDay.ActiveInstallations7d)
	}
	if countsNextDay.ActiveInstallations30d != 2 {
		t.Errorf("ActiveInstallations30d within 30d block = %d, want 2", countsNextDay.ActiveInstallations30d)
	}

	// Far future date: all epochs have rotated
	farFuture := now.AddDate(0, 2, 0)
	countsFuture, err := store.NetworkCounts(ctx, farFuture)
	if err != nil {
		t.Fatalf("counts far future: %v", err)
	}
	if countsFuture.ActiveInstallations1d != 0 || countsFuture.ActiveInstallations7d != 0 || countsFuture.ActiveInstallations30d != 0 {
		t.Fatalf("future counts not zero: %+v", countsFuture)
	}

	// 6. Prune retention semantics
	// Cutoff before records were created: 0 removed
	removed0, err := store.PrunePresence(ctx, now.AddDate(0, 0, 10), 40, 100)
	if err != nil {
		t.Fatalf("prune before cutoff: %v", err)
	}
	if removed0 != 0 {
		t.Fatalf("removed %d records before cutoff, want 0", removed0)
	}

	// Cutoff 45 days in future: all records (7 reports * 3 intervals = 21 records) should be eligible
	removedExpired, err := store.PrunePresence(ctx, now.AddDate(0, 0, 45), 40, 100)
	if err != nil {
		t.Fatalf("prune expired: %v", err)
	}
	if removedExpired == 0 {
		t.Fatalf("expected expired records to be pruned, got 0")
	}
}

func TestFakePresenceStoreContract(t *testing.T) {
	runPresenceStoreContract(t, NewFake())
}

func TestIntegrationPGPresenceStoreMatchesTheFake(t *testing.T) {
	runPresenceStoreContract(t, openTestPG(t))
}
