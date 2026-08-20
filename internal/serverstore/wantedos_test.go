package serverstore

import (
	"context"
	"testing"
)

func seedAnswer(t *testing.T, f *Fake, id, purl, os string) {
	t.Helper()
	ctx := context.Background()
	if err := f.SaveSample(ctx, SampleRow{SampleID: id,
		ManifestJSON: `{"packages":["` + purl + `"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{ReceiptID: "r-" + id, SampleID: id,
		ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS"},` +
			`"resolvedPackages":["` + purl + `"],"environment":{"os":"` + os + `"}}`}); err != nil {
		t.Fatal(err)
	}
}

// Somebody reported this breaking on Windows. A Linux proof does not answer
// that, and closing the row on one would delete the ask before the platform
// it was about had ever been measured — the same mistake that let the network
// observe thousands of packages on Windows while proving none of them there.
func TestWantedPinnedToAnOSIsNotClosedByAnotherOSProof(t *testing.T) {
	ctx := context.Background()
	purl := "pkg:golang/example.com/mod@v1.0.0"

	f := NewFake()
	if err := f.RecordWanted(ctx, "2026-08-20", "aaaabbbbccccdddd", []WantedRow{
		{Ecosystem: "golang", Name: "example.com/mod", Version: "v1.0.0", TargetOS: "windows"},
	}); err != nil {
		t.Fatal(err)
	}
	seedAnswer(t, f, "sha256:onlinux", purl, "linux")

	rows, err := f.TopWanted(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("a Windows ask was closed by a Linux proof: %+v", rows)
	}
	if rows[0].TargetOS != "windows" {
		t.Errorf("row lost its target OS: %+v", rows[0])
	}

	seedAnswer(t, f, "sha256:onwindows", purl, "windows")
	rows, err = f.TopWanted(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("a Windows proof did not close the Windows ask: %+v", rows)
	}
}

// Two platforms failing the same release are two different questions, and
// folding them into one row loses whichever was reported second.
func TestWantedCountsEachOSSeparately(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	for _, os := range []string{"windows", "linux"} {
		if err := f.RecordWanted(ctx, "2026-08-20", "aaaabbbbccccdddd", []WantedRow{
			{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", TargetOS: os},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.TopWanted(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want one per reported OS: %+v", len(rows), rows)
	}
}
