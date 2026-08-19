package serverstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// expansionStore is the slice of a store this parity check needs. Both the
// Fake and PG satisfy it.
type expansionStore interface {
	IngestBatches(context.Context, []domain.ObservationBatch) (int, []RejectedBatch, error)
	UpsertPackage(context.Context, PackageRow) error
	SaveSample(context.Context, SampleRow) error
	SaveReceipt(context.Context, ReceiptRow) error
	ListAuthoringExpansionCandidates(context.Context, int) ([]WantedRow, error)
}

// seedExpansion writes one scenario into whichever store it is given, so the
// Fake and PG are compared on identical input rather than on two hand-written
// fixtures that quietly drift apart.
func seedExpansion(t *testing.T, store expansionStore, rows []expansionSeed) {
	t.Helper()
	ctx := context.Background()
	for _, r := range rows {
		env := domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: r.os, Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
		}
		b := domain.ObservationBatch{
			SchemaVersion: 1, Epoch: "2026-08-19", AnonID: r.anon, ProjectBucket: r.anon + "proj",
			Package: "pkg:npm/" + r.name + "@" + r.version, Symbol: r.symbol,
			SymbolConfidence: domain.SymbolProbable, Environment: env,
			Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: r.count,
		}
		if accepted, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{b}); err != nil || accepted != 1 || len(rejected) != 0 {
			t.Fatalf("ingest %s: accepted=%d rejected=%v err=%v", r.name, accepted, rejected, err)
		}
		if err := store.UpsertPackage(ctx, PackageRow{
			PURL: "pkg:npm/" + r.name + "@" + r.version, Ecosystem: "npm", Name: r.name,
			Version: r.version, Major: r.version[:1], Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
		if r.proven {
			id := "sha256:proof-" + r.name + "-" + r.version
			if err := store.SaveSample(ctx, SampleRow{
				SampleID:     id,
				ManifestJSON: `{"packages":["pkg:npm/` + r.name + `@` + r.version + `"],"symbols":[]}`,
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveReceipt(ctx, ReceiptRow{
				SampleID: id, ReceiptID: "receipt-" + r.name + "-" + r.version,
				PeerID: "peer-" + r.name, EnvHash: "env-" + r.name + "-" + r.version,
				ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"` + r.os + `"}}`,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type expansionSeed struct {
	name, version, symbol, os, anon string
	count                           int
	proven                          bool
}

func candidateLine(r WantedRow) string {
	symbol := r.Symbol
	if symbol == "" {
		symbol = "(package)"
	}
	return fmt.Sprintf("%s@%s/%s %s score=%d os=%s", r.Name, r.Version, symbol, r.Kind, r.Score, r.TargetOS)
}

// The Fake exists so httpapi tests can assert what production would do. When
// the two disagree, a test can prove an assignment the server would never
// make -- so the orders are compared row for row, on scenarios chosen because
// they separate the two ranking rules that had drifted: the sibling branch's
// rank against a scored symbol, and whether depth counting is OS-aware.
func TestIntegrationAuthoringExpansionFakeMatchesPostgres(t *testing.T) {
	scenarios := []struct {
		name string
		seed []expansionSeed
	}{
		{
			// A scored symbol job against an unmeasured sibling: the symbol
			// outranks the sibling, whose branch sorts last on merit.
			name: "sibling versus scored symbol",
			seed: []expansionSeed{
				{name: "cpkg", version: "1.0.0", symbol: "c.sym", os: "linux", anon: "cpeer", count: 77, proven: true},
				{name: "cpkg", version: "2.0.0", symbol: "c.sym", os: "linux", anon: "cpeer2", count: 1},
			},
		},
		{
			// Depth must not be counted OS-first: a windows row must not take
			// depth 1 away from a linux row of the same version.
			name: "depth counting is OS independent",
			seed: []expansionSeed{
				{name: "dpkg", version: "1.0.0", symbol: "d.win", os: "windows", anon: "dwin", count: 900, proven: true},
				{name: "dpkg", version: "1.0.0", symbol: "d.lin", os: "linux", anon: "dlin", count: 10},
				{name: "epkg", version: "1.0.0", symbol: "e.sym", os: "linux", anon: "epeer", count: 5, proven: true},
			},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			ctx := context.Background()
			fake := NewFake()
			fake.NowFn = func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) }
			seedExpansion(t, fake, sc.seed)
			pg := openTestPG(t)
			seedExpansion(t, pg, sc.seed)

			fakeRows, err := fake.ListAuthoringExpansionCandidates(ctx, 50)
			if err != nil {
				t.Fatal(err)
			}
			pgRows, err := pg.ListAuthoringExpansionCandidates(ctx, 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(fakeRows) != len(pgRows) {
				t.Fatalf("row count differs: fake=%d pg=%d\n fake: %v\n pg:   %v",
					len(fakeRows), len(pgRows), formatCandidateOrder(fakeRows), formatCandidateOrder(pgRows))
			}
			for i := range pgRows {
				if got, want := candidateLine(fakeRows[i]), candidateLine(pgRows[i]); got != want {
					t.Errorf("row %d differs\n  fake: %s\n  pg:   %s", i, got, want)
				}
			}
		})
	}
}
