package serverstore

import (
	"context"
	"reflect"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// One symbol, two spellings, and a cube that showed a dash where its evidence
// was.
//
// Snapshot targets come from two places. evidence_agg carries the name the
// SCANNER wrote, which for Go is the module path and the symbol —
// "github.com/google/uuid.New". A sample manifest carries the name the AUTHOR
// wrote, which is usually bare — "MarshalText". Both land in the same target
// set, so the same symbol exists under two keys and a lookup by the author's
// spelling matches nothing.
//
// Measured in production: of 1,235 golang symbol coordinates only 287 found
// any observation, and every golang row in evidence_agg was qualified while
// 630 of the coordinates were bare. Those coordinates were not reporting "no
// observations"; they were reporting that nobody had reconciled two spellings.
func TestSymbolSpellingsCoversTheScannerAndTheAuthor(t *testing.T) {
	for _, c := range []struct {
		name, purl, symbol string
		want               []string
	}{
		{
			name: "a bare Go symbol also answers to the qualified form",
			purl: "pkg:golang/github.com/google/uuid@v1.6.0", symbol: "MarshalText",
			want: []string{"MarshalText", "github.com/google/uuid.MarshalText"},
		},
		{
			name: "an already-qualified symbol is not qualified twice",
			purl: "pkg:golang/github.com/google/uuid@v1.6.0", symbol: "github.com/google/uuid.New",
			want: []string{"github.com/google/uuid.New"},
		},
		{
			name: "npm reads the same way",
			purl: "pkg:npm/semver@7.7.1", symbol: "coerce",
			want: []string{"coerce", "semver.coerce"},
		},
		{
			name: "the package-level row has no symbol to qualify",
			purl: "pkg:npm/semver@7.7.1", symbol: "",
			want: []string{""},
		},
		{
			name: "an unparseable purl still answers to what was asked",
			purl: "not-a-purl", symbol: "New",
			want: []string{"New"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := symbolSpellings(c.purl, c.symbol); !reflect.DeepEqual(got, c.want) {
				t.Errorf("symbolSpellings(%q, %q) = %v, want %v", c.purl, c.symbol, got, c.want)
			}
		})
	}
}

// And the store uses it: a bare coordinate finds evidence the scanner filed
// under the qualified name.
func TestFakeFindsEvidenceFiledUnderTheOtherSpelling(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	const purl = "pkg:golang/github.com/google/uuid@v1.6.0"

	env := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", OS: "linux", Arch: "x64"}.Normalize()
	if n, rejected, err := f.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1,
		Epoch:         "2026-08-22",
		AnonID:        "anon-1",
		ProjectBucket: "bucket-1",
		Package:       purl,
		// As the scanner files it.
		Symbol:           "github.com/google/uuid.MarshalText",
		SymbolConfidence: domain.SymbolExact,
		Environment:      env,
		Stage:            domain.StageProjectTest,
		Result:           domain.ResultPass,
		ObservationCount: 3,
	}}); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("ingest accepted %d batches, rejected %+v", n, rejected)
	}

	// As a sample manifest names it.
	rows, err := f.EvidenceForTarget(ctx, purl, "MarshalText")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("a bare symbol coordinate found none of its own evidence")
	}
}
