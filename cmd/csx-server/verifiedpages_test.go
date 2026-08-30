package main

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Publication makes source inspectable; only a contract-PASS receipt makes
// it verified or turns its author prose into a measured public finding.
func TestPackageSamplesAndDerivedFindingsRequireContractPass(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	w := &webStore{s: store}
	manifest := `{
		"schemaVersion":1,
		"packages":["pkg:npm/axios@1.12.0"],
		"symbols":["axios.get"],
		"case":{"schemaVersion":1,"kind":"HOW","goal":"request once",
			"believed":"axios retries automatically",
			"packages":["pkg:npm/axios@1.12.0"],
			"contract":["the server receives one request and no automatic retry is added"]}}
	`
	for _, id := range []string{"sha256:source-only", "sha256:proved"} {
		if err := store.SaveSample(ctx, serverstore.SampleRow{
			SampleID: id, ManifestJSON: manifest, Status: "PUBLISHED",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID: "receipt-proved", SampleID: "sha256:proved",
		ContractResult: "PASS", ReceiptJSON: `{}`,
	}); err != nil {
		t.Fatal(err)
	}

	samples, err := w.PackageSamples(ctx, "npm", "axios", 25)
	if err != nil || len(samples) != 1 || samples[0].SampleID != "sha256:proved" {
		t.Fatalf("package samples = %+v, err=%v", samples, err)
	}
	findings, err := w.DerivedFindings(ctx)
	if err != nil || len(findings) != 1 || findings[0].SampleID != "sha256:proved" {
		t.Fatalf("derived findings = %+v, err=%v", findings, err)
	}
}
