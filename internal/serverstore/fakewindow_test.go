package serverstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// seedSamples writes n live samples, oldest first, so the one being looked
// for is well outside any newest-N window.
func seedSamples(t *testing.T, f *Fake, n int, targetIdx int, targetPkg, targetSeeder string) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		pkg, seeder := "pkg:npm/filler-"+fmt.Sprint(i)+"@1.0.0", "someone-else"
		if i == targetIdx {
			pkg, seeder = targetPkg, targetSeeder
		}
		row := SampleRow{
			SampleID:     fmt.Sprintf("sha256:%064d", i),
			Status:       "PUBLISHED",
			License:      "MIT-0",
			OriginSeeder: seeder,
			CreatedAt:    base.Add(time.Duration(i) * time.Minute),
			ManifestJSON: `{"schemaVersion":1,"packages":["` + pkg + `"],` +
				`"case":{"schemaVersion":1,"kind":"HOW","goal":"g","packages":["` + pkg + `"],"contract":["c"]}}`,
		}
		if err := f.SaveSample(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
}

// The Fake is what handler tests are held to, so a query it answers from
// only the newest fifty rows cannot fail the test that would catch the
// regression Postgres was fixed for: search used to score the newest 500
// globally, which made relevance a function of publication order.
func TestFakeSamplesForPackagesSeesPastTheDefaultWindow(t *testing.T) {
	f := NewFake()
	const target = "pkg:npm/axios@1.12.0"
	// Index 0 is the OLDEST, so it sits below any newest-N window.
	seedSamples(t, f, 120, 0, target, "someone-else")

	rows, err := f.SamplesForPackages(context.Background(), []string{"pkg:npm/axios@%"}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("found %d samples naming axios, want 1 — the oldest matching "+
			"sample must still be findable once 120 exist", len(rows))
	}
}

func TestFakeSamplesBySeederSeesPastTheDefaultWindow(t *testing.T) {
	f := NewFake()
	seedSamples(t, f, 120, 0, "pkg:npm/left-pad@1.3.0", "millwright")

	rows, err := f.SamplesBySeeder(context.Background(), "millwright", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("a seeder's oldest sample vanished from their own page: got %d", len(rows))
	}
}

// The limit the caller passes must still be honoured — removing the hidden
// cap must not remove the real one.
func TestFakeSamplesForPackagesStillHonoursTheCallersLimit(t *testing.T) {
	f := NewFake()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if err := f.SaveSample(context.Background(), SampleRow{
			SampleID:  fmt.Sprintf("sha256:%064d", i),
			Status:    "PUBLISHED",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			ManifestJSON: `{"schemaVersion":1,"packages":["pkg:npm/axios@1.12.0"],` +
				`"case":{"schemaVersion":1,"kind":"HOW","goal":"g","packages":["pkg:npm/axios@1.12.0"],"contract":["c"]}}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.SamplesForPackages(context.Background(), []string{"pkg:npm/axios@%"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Errorf("returned %d rows for a limit of 3", len(rows))
	}
}

func TestFakeVerifiedSampleReadsRequireContractPass(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	manifest := `{"schemaVersion":1,"packages":["pkg:npm/axios@1.12.0"],` +
		`"case":{"schemaVersion":1,"kind":"HOW","goal":"g","packages":["pkg:npm/axios@1.12.0"],"contract":["c"]}}`
	for _, id := range []string{"sha256:source-only", "sha256:proved"} {
		if err := f.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: manifest}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "receipt-pass", SampleID: "sha256:proved", ContractResult: "PASS", ReceiptJSON: `{}`,
	}); err != nil {
		t.Fatal(err)
	}

	byPackage, err := f.VerifiedSamplesForPackages(ctx, []string{"pkg:npm/axios@%"}, 10)
	if err != nil || len(byPackage) != 1 || byPackage[0].SampleID != "sha256:proved" {
		t.Fatalf("verified package rows = %+v, err=%v", byPackage, err)
	}
	all, err := f.ListVerifiedSamples(ctx, 10)
	if err != nil || len(all) != 1 || all[0].SampleID != "sha256:proved" {
		t.Fatalf("verified sample rows = %+v, err=%v", all, err)
	}
}

func TestFakeWantedVersionUsesResolvedReceiptNotManifestVersion(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	wanted := WantedRow{
		Ecosystem: "npm", Name: "three", Version: "0.180.0", Symbol: "Texture.transformUv",
	}
	if err := f.RecordWanted(ctx, "2026-08-17", "anon-a", []WantedRow{wanted}); err != nil {
		t.Fatal(err)
	}

	// The manifest says the requested version, but the resolver proved that
	// this PASS ran against another release. It must stay wanted.
	if err := f.SaveSample(ctx, SampleRow{
		SampleID: "sha256:wrong-resolve",
		ManifestJSON: `{"packages":["pkg:npm/three@0.180.0"],` +
			`"symbols":["Texture.transformUv"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "receipt-wrong", SampleID: "sha256:wrong-resolve", ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS","contract":"PASS"},` +
			`"resolvedPackages":["pkg:npm/three@0.179.0"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if rows, err := f.TopWanted(ctx, 10); err != nil || len(rows) != 1 {
		t.Fatalf("manifest claim falsely closed exact request: rows=%+v err=%v", rows, err)
	}

	// A matrix run may declare a base release while resolving the requested
	// one. The exact signed resolved package is what closes the row.
	if err := f.SaveSample(ctx, SampleRow{
		SampleID: "sha256:matrix",
		ManifestJSON: `{"packages":["pkg:npm/three@0.179.0"],` +
			`"symbols":["Texture.transformUv"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "receipt-matrix", SampleID: "sha256:matrix", ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS","contract":"PASS"},` +
			`"resolvedPackages":["pkg:npm/three@0.180.0"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if rows, err := f.TopWanted(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatalf("exact matrix receipt did not close request: rows=%+v err=%v", rows, err)
	}
}

func TestFakeLegacyUnversionedWantedUsesAnyPackageContractPass(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	if err := f.RecordWanted(ctx, "2026-08-17", "anon-a", []WantedRow{{
		Ecosystem: "npm", Name: "three", Symbol: "LegacySymbol",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveSample(ctx, SampleRow{
		SampleID:     "sha256:legacy",
		ManifestJSON: `{"packages":["pkg:npm/three@0.170.0"],"symbols":["LegacySymbol"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	// v1 had no resolvedPackages field. The migration cannot recover the
	// version of an old Wanted row either, so same-package contract proof is
	// the intentionally broad legacy policy.
	if err := f.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "receipt-legacy", SampleID: "sha256:legacy", ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":1,"stages":{"contract":"PASS"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if rows, err := f.TopWanted(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatalf("legacy unversioned request stayed open: rows=%+v err=%v", rows, err)
	}
}
