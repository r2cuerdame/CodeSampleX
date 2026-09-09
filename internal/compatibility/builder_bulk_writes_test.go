package compatibility

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// receiptResolvedSamples is one sample per package, each with a receipt that
// resolved exactly that package: the shape that makes the builder register
// receipt-only releases.
func receiptResolvedSamples(t *testing.T, n int) ([]sampleData, []domain.PURL) {
	t.Helper()
	samples := make([]sampleData, 0, n)
	purls := make([]domain.PURL, 0, n)
	for i := 0; i < n; i++ {
		p, err := domain.ParsePURL(fmt.Sprintf("pkg:npm/registered%05d@1.0.0", i))
		if err != nil {
			t.Fatal(err)
		}
		purls = append(purls, p)
		samples = append(samples, sampleData{
			row:      serverstore.SampleRow{SampleID: fmt.Sprintf("sha256:%064x", 400000+i)},
			receipts: []ReceiptInfo{{ResolvedPackages: []domain.PURL{p}}},
		})
	}
	return samples, purls
}

// registerKnown gives the first `known` purls a PUBLIC row before the pass,
// at an earlier clock, so that "left completely alone" is observable as an
// unchanged last_seen.
func registerKnown(t *testing.T, fake *serverstore.Fake, purls []domain.PURL, known int) {
	t.Helper()
	for _, p := range purls[:known] {
		if err := fake.UpsertPackage(context.Background(), serverstore.PackageRow{
			PURL: p.String(), Ecosystem: p.Ecosystem, Name: p.Name, Version: p.Version,
			Major: p.Major(), Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// Registering receipt-only packages wrote one UpsertPackage per unknown
// release. The membership question was already moved to one bounded page
// (#174); the registration behind it was still a checkout per row, so the
// first pass over a corpus with thousands of receipt-resolved releases was
// thousands of background checkouts against the same small pool. The rows
// written must be the same rows, and known packages must stay untouched.
func TestReceiptPackageRegistrationWritesUnknownRowsInBoundedPages(t *testing.T) {
	const n = 2*packageRegisterBatch + 5
	const known = 3
	ctx := context.Background()
	samples, purls := receiptResolvedSamples(t, n+known)

	seeded := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	passAt := seeded.Add(48 * time.Hour)
	run := func(name string, store func(*builderReadCounter) serverstore.Store) (*serverstore.Fake, *builderReadCounter) {
		t.Helper()
		fake := serverstore.NewFake()
		fake.NowFn = func() time.Time { return seeded }
		registerKnown(t, fake, purls, known)
		fake.NowFn = func() time.Time { return passAt }
		counter := newReadCounter(fake)
		if err := (&Builder{Store: store(counter)}).ensureReceiptPackages(ctx, samples); err != nil {
			t.Fatalf("%s: ensureReceiptPackages: %v", name, err)
		}
		return fake, counter
	}
	rowFake, rowCounter := run("row-at-a-time", func(c *builderReadCounter) serverstore.Store { return &rowAtATimeStore{c} })
	bulkFake, bulkCounter := run("bulk", func(c *builderReadCounter) serverstore.Store { return &bulkReadStore{c} })

	for _, p := range purls {
		rowPkg, rowOK, _ := rowFake.GetPackage(ctx, p.String())
		bulkPkg, bulkOK, _ := bulkFake.GetPackage(ctx, p.String())
		if !rowOK || !bulkOK || rowPkg != bulkPkg {
			t.Fatalf("%s: bounded-page registration wrote a different row:\nrow-at-a-time %+v ok=%t\nbulk          %+v ok=%t",
				p, rowPkg, rowOK, bulkPkg, bulkOK)
		}
	}
	for _, p := range purls[:known] {
		pkg, _, _ := bulkFake.GetPackage(ctx, p.String())
		if pkg.Publicness != "PUBLIC" || !pkg.LastSeen.Equal(seeded) {
			t.Fatalf("%s was already known and must be left alone, got %+v", p, pkg)
		}
	}
	for _, p := range purls[known:] {
		pkg, ok, _ := bulkFake.GetPackage(ctx, p.String())
		if !ok || pkg.Publicness != "UNKNOWN" || !pkg.FirstSeen.Equal(passAt) {
			t.Fatalf("%s was not registered as an UNKNOWN release on this pass, got %+v ok=%t", p, pkg, ok)
		}
	}

	t.Logf("%d unknown of %d resolved: row-at-a-time UpsertPackage=%d; bulk UpsertPackages=%v UpsertPackage=%d",
		n, n+known, rowCounter.count("UpsertPackage"), bulkCounter.sizes("UpsertPackages"), bulkCounter.count("UpsertPackage"))
	if got, want := rowCounter.count("UpsertPackage"), n; got != want {
		t.Fatalf("row-at-a-time registrations = %d, want %d", got, want)
	}
	if got := bulkCounter.count("UpsertPackage"); got != 0 {
		t.Fatalf("bulk store still registered %d packages one at a time", got)
	}
	if got, want := bulkCounter.sizes("UpsertPackages"), []int{packageRegisterBatch, packageRegisterBatch, 5}; !equalInts(got, want) {
		t.Fatalf("registration pages = %v, want %v", got, want)
	}
}

// Nothing unknown, nothing written: a pass over a fully registered corpus
// must not open a write checkout at all.
func TestReceiptPackageRegistrationWritesNothingWhenEverythingIsKnown(t *testing.T) {
	samples, purls := receiptResolvedSamples(t, 40)
	fake := serverstore.NewFake()
	registerKnown(t, fake, purls, len(purls))
	counter := newReadCounter(fake)
	if err := (&Builder{Store: &bulkReadStore{counter}}).ensureReceiptPackages(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	if got := counter.count("UpsertPackages") + counter.count("UpsertPackage"); got != 0 {
		t.Fatalf("a fully registered corpus opened %d write pages", got)
	}
}

// A refused page is a failed pass, not a quietly unregistered release: a
// receipt-only version the registry endpoints cannot see is exactly the
// defect this registration exists to prevent.
func TestReceiptPackageRegistrationFailureStopsThePass(t *testing.T) {
	samples, _ := receiptResolvedSamples(t, 7)
	store := &failingBulkStore{Fake: serverstore.NewFake(), failPackageWrites: true}
	err := (&Builder{Store: store}).ensureReceiptPackages(context.Background(), samples)
	if err == nil || !strings.Contains(err.Error(), "register receipt packages") {
		t.Fatalf("a refused registration page was not reported: %v", err)
	}
	if !errors.Is(err, errPoolExhausted) {
		t.Fatalf("the store's own error was not preserved: %v", err)
	}
}
