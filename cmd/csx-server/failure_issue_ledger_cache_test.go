package main

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func failureIssueLedgerRows(n int) []serverstore.ClusterRow {
	rows := make([]serverstore.ClusterRow, n)
	for i := range rows {
		rows[i] = serverstore.ClusterRow{
			Ecosystem: "npm", PackageName: "many", Stage: "PROJECT_TEST",
			ErrorFingerprint: "sha256:failure", ObservationCount: int64(i + 1),
			EnvVariantsJSON: `[{"os":"linux","node":"22"}]`, VersionsJSON: `["1.0.0","1.0.1"]`,
		}
	}
	return rows
}

func TestFailureIssueLedgerIsReusedAcrossIssueURLs(t *testing.T) {
	store := &completeFailureClusterStore{Fake: serverstore.NewFake(), rows: failureIssueLedgerRows(600)}
	w := &webStore{s: store}
	first, err := w.FailureIssueClusters(t.Context(), "npm", "many")
	if err != nil || len(first) != 600 {
		t.Fatalf("first ledger = %d, err=%v", len(first), err)
	}
	for range 20 {
		docs, err := w.FailureIssueClusters(t.Context(), "npm", "many")
		if err != nil || len(docs) != 600 || docs[599] != first[599] {
			t.Fatalf("cached ledger = %d, err=%v", len(docs), err)
		}
	}
	if store.completeCalls != 1 {
		t.Fatalf("complete ledger reads = %d, want 1", store.completeCalls)
	}
	page, matched, err := w.FailureClusters(t.Context(), "npm", "many")
	if err != nil || len(page) != maxClustersToPage || matched != 600 || store.pageCalls != 1 {
		t.Fatalf("display page shares a key with the complete ledger: page=%d matched=%d calls=%d err=%v", len(page), matched, store.pageCalls, err)
	}
}

func TestFailureIssueLedgerExpiresAfterTTL(t *testing.T) {
	store := &completeFailureClusterStore{Fake: serverstore.NewFake(), rows: failureIssueLedgerRows(3)}
	w := &webStore{s: store}
	if _, err := w.FailureIssueClusters(t.Context(), "npm", "many"); err != nil {
		t.Fatal(err)
	}
	key := "\x00complete-issue-ledger|npm|many"
	entry, ok := w.loadFailureClusterPage(key)
	if !ok {
		t.Fatal("complete ledger was not retained")
	}
	entry.at = time.Now().Add(-failureIssueLedgerTTL - time.Second)
	w.cacheFailureClusterPage(key, entry)
	store.rows = failureIssueLedgerRows(4)
	docs, err := w.FailureIssueClusters(t.Context(), "npm", "many")
	if err != nil || len(docs) != 4 || store.completeCalls != 2 {
		t.Fatalf("expired ledger = %d docs, reads=%d, err=%v", len(docs), store.completeCalls, err)
	}
}

func TestFailureIssueLedgerErrorIsNotCached(t *testing.T) {
	store := &failingLedgerStore{Fake: serverstore.NewFake()}
	w := &webStore{s: store}
	if _, err := w.FailureIssueClusters(t.Context(), "npm", "many"); err == nil {
		t.Fatal("store error was hidden")
	}
	if _, err := w.FailureIssueClusters(t.Context(), "npm", "many"); err == nil || store.calls != 2 {
		t.Fatalf("failed read was cached: calls=%d err=%v", store.calls, err)
	}
}

type failingLedgerStore struct {
	*serverstore.Fake
	calls int
}

func (s *failingLedgerStore) ListFailureClusters(context.Context, string) ([]serverstore.ClusterRow, error) {
	s.calls++
	return nil, context.DeadlineExceeded
}

func BenchmarkFailureIssueClustersRepeatedIssueURL(b *testing.B) {
	w := &webStore{s: &completeFailureClusterStore{Fake: serverstore.NewFake(), rows: failureIssueLedgerRows(600)}}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if docs, err := w.FailureIssueClusters(ctx, "npm", "many"); err != nil || len(docs) != 600 {
			b.Fatal(err)
		}
	}
}
