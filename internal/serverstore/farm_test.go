package serverstore

import (
	"context"
	"testing"
	"time"
)

// A session issued but never refreshed is a worker that failed to start. It
// looks identical to a healthy one in every list the dashboard had, which is
// how csx-farm-windows-1 sat dead for half an hour while its slot showed as
// issued.
func TestFarmWorkersDistinguishesAWorkerThatNeverCameAlive(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 17, 30, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }

	if err := store.IssueAuthoringSessions(ctx, []AuthoringSessionRow{
		{TokenHash: "h-alive", SessionID: "alive", Label: "linux-slot1", Model: "agy",
			Reasoning: "auto", IssuedAt: now.Add(-2 * time.Hour), IdleExpiresAt: now.Add(time.Hour)},
		{TokenHash: "h-dead", SessionID: "dead", Label: "windows-slot1", Model: "agy",
			Reasoning: "auto", IssuedAt: now.Add(-25 * time.Minute), IdleExpiresAt: now.Add(35 * time.Minute)},
	}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshAuthoringSession(ctx, "h-alive", "10.0.0.1", "csx-farm-linux-1",
		now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	workers, err := store.FarmWorkers(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	byLabel := map[string]FarmWorker{}
	for _, w := range workers {
		byLabel[w.Label] = w
	}
	alive, ok := byLabel["linux-slot1"]
	if !ok {
		t.Fatalf("the live worker is missing: %+v", workers)
	}
	if alive.LastRefreshAt.IsZero() {
		t.Error("a worker that refreshed reports no refresh")
	}
	if alive.ComputerName != "csx-farm-linux-1" {
		t.Errorf("computer name = %q, want the name the worker reported", alive.ComputerName)
	}
	dead, ok := byLabel["windows-slot1"]
	if !ok {
		t.Fatalf("the never-started worker is missing entirely: %+v", workers)
	}
	if !dead.LastRefreshAt.IsZero() {
		t.Errorf("a worker that never refreshed reports one: %s", dead.LastRefreshAt)
	}
}

// The duplicate rate is the number that sat at 37% for a day with nowhere to
// show itself.
func TestFarmHealthCountsDuplicateCoordinatesAndOSCoverage(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 17, 30, 0, 0, time.UTC)

	// Two samples answer the same purl and symbols; a third answers another.
	for _, s := range []struct{ id, manifest, os string }{
		{"sha256:a", `{"packages":["pkg:npm/dup@1.0.0"],"symbols":["dup.call"]}`, "linux"},
		{"sha256:b", `{"packages":["pkg:npm/dup@1.0.0"],"symbols":["dup.call"]}`, "linux"},
		{"sha256:c", `{"packages":["pkg:npm/solo@1.0.0"],"symbols":["solo.call"]}`, "windows"},
	} {
		if err := store.SaveSample(ctx, SampleRow{SampleID: s.id, ManifestJSON: s.manifest}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveReceipt(ctx, ReceiptRow{
			ReceiptID: "r-" + s.id, SampleID: s.id, ContractResult: "PASS",
			ReceiptJSON: `{"environment":{"os":"` + s.os + `"}}`,
		}); err != nil {
			t.Fatal(err)
		}
	}

	health, err := store.FarmHealthNow(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if health.PublicSamples != 3 {
		t.Errorf("public samples = %d, want 3", health.PublicSamples)
	}
	if health.DuplicateCoords != 1 {
		t.Errorf("duplicate coordinates = %d, want 1", health.DuplicateCoords)
	}
	if health.ReceiptsByOS["linux"] != 2 || health.ReceiptsByOS["windows"] != 1 {
		t.Errorf("receipts by OS = %v, want linux 2 and windows 1", health.ReceiptsByOS)
	}
}
