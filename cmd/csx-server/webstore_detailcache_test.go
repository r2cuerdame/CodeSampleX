package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type countingPackageDetailStore struct {
	*serverstore.Fake
	versionReads atomic.Int64
	clusterReads atomic.Int64
}

func (s *countingPackageDetailStore) ListPackageVersions(ctx context.Context, ecosystem, name string) ([]serverstore.PackageRow, error) {
	s.versionReads.Add(1)
	return s.Fake.ListPackageVersions(ctx, ecosystem, name)
}

func (s *countingPackageDetailStore) ListFailureClusters(context.Context, string) ([]serverstore.ClusterRow, error) {
	s.clusterReads.Add(1)
	return []serverstore.ClusterRow{{
		Ecosystem: "golang", PackageName: "github.com/jackc/pgx/v5",
		Symbol: "pgx.Connect", Stage: "contract", ObservationCount: 3,
	}}, nil
}

// Package, version and symbol routes ask for these two immutable builder
// products repeatedly. At production cardinality each pool reacquisition can
// queue behind the builder, so one process should read each product once per
// builder interval rather than once per route (and versions twice per cube).
func TestPackageDetailReadsAreReusedForTheBuilderInterval(t *testing.T) {
	ctx := context.Background()
	store := &countingPackageDetailStore{Fake: serverstore.NewFake()}
	row := serverstore.PackageRow{
		PURL:      "pkg:golang/github.com/jackc/pgx/v5@v5.10.0",
		Ecosystem: "golang", Name: "github.com/jackc/pgx/v5", Version: "v5.10.0",
		Major: "5", Publicness: "PUBLIC", FirstSeen: time.Now(), LastSeen: time.Now(),
	}
	if err := store.UpsertPackage(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(ctx, row.PURL, "", `{"rows":[]}`); err != nil {
		t.Fatal(err)
	}

	w := &webStore{s: store}
	for range 3 {
		versions, err := w.PackageVersions(ctx, row.Ecosystem, row.Name)
		if err != nil || len(versions) != 1 {
			t.Fatalf("PackageVersions = %v, err=%v", versions, err)
		}
		docs, matched, err := w.FailureClusters(ctx, row.Ecosystem, row.Name)
		if err != nil || len(docs) != 1 || matched != 1 {
			t.Fatalf("FailureClusters = %d docs, matched=%d, err=%v", len(docs), matched, err)
		}
	}
	if got := store.versionReads.Load(); got != 1 {
		t.Errorf("package version reads = %d, want 1", got)
	}
	if got := store.clusterReads.Load(); got != 1 {
		t.Errorf("failure cluster reads = %d, want 1", got)
	}
}
