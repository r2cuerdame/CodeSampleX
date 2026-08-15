package serverstore

import (
	"context"
	"testing"
)

// A sample id is the sha256 of its content, so a second publish of the same
// id is the same sample arriving again — an author re-running their
// pipeline, which is the normal way a seeder works. The ingest path always
// sends status "PUBLISHED", and the upsert wrote it over whatever was
// there.
//
// So CROSS_PASS, MATRIX_PASS or STABLE — verification independent peers
// actually performed and signed receipts for — was discarded by the author
// pressing publish again. The receipts survived, so an operator could
// recover it by running recompute-status by hand; until then the sample
// ranked lower everywhere, and under the shard cap it could be cut from its
// own shard in favour of unverified ones.
func TestRepublishingDoesNotWalkBackAnEarnedStatus(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	const id = "sha256:aaaa"

	if err := f.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: "{}", Status: "PUBLISHED"}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetSampleStatus(ctx, id, "CROSS_PASS"); err != nil {
		t.Fatal(err)
	}

	// The author publishes the identical sample again.
	if err := f.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: "{}", Status: "PUBLISHED"}); err != nil {
		t.Fatal(err)
	}

	row, ok, err := f.GetSample(ctx, id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if row.Status != "CROSS_PASS" {
		t.Errorf("status after re-publish = %q, want CROSS_PASS kept", row.Status)
	}
}

// A quarantine must survive it too, or a takedown is undone by whoever
// published the sample in the first place.
func TestRepublishingDoesNotLiftAQuarantine(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	const id = "sha256:bbbb"

	if err := f.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: "{}", Status: "PUBLISHED"}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetSampleQuarantine(ctx, id, true, "takedown"); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: "{}", Status: "PUBLISHED"}); err != nil {
		t.Fatal(err)
	}

	row, ok, err := f.GetSample(ctx, id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if !row.Quarantined {
		t.Error("re-publishing lifted the quarantine")
	}
}
