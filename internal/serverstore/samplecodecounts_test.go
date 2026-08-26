package serverstore

import (
	"context"
	"reflect"
	"testing"
)

type verifiedSampleCodeCountStore interface {
	SaveSample(context.Context, SampleRow) error
	SaveReceipt(context.Context, ReceiptRow) error
	VerifiedSampleCodeCounts(context.Context, string) ([]VerifiedSampleCodeCount, error)
}

func seedVerifiedSampleCodeCounts(t *testing.T, store verifiedSampleCodeCountStore) {
	t.Helper()
	ctx := context.Background()
	samples := []struct {
		row     SampleRow
		verdict string
		receipt bool
	}{
		{SampleRow{SampleID: "sha256:code-a", ManifestJSON: `{"packages":["pkg:npm/axios@1.0.0","pkg:npm/axios@1.0.0"],"symbols":["axios.get","axios.get","axios.post"]}`}, "PASS", true},
		{SampleRow{SampleID: "sha256:code-b", ManifestJSON: `{"packages":["pkg:npm/axios@1.0.0"],"symbols":["axios.get"]}`}, "PASS", true},
		{SampleRow{SampleID: "sha256:code-v2", ManifestJSON: `{"packages":["pkg:npm/axios@2.0.0"],"symbols":[]}`}, "PASS", true},
		{SampleRow{SampleID: "sha256:failed", ManifestJSON: `{"packages":["pkg:npm/axios@1.0.0"],"symbols":["axios.delete"]}`}, "FAIL", true},
		{SampleRow{SampleID: "sha256:unverified", ManifestJSON: `{"packages":["pkg:npm/axios@1.0.0"],"symbols":["axios.patch"]}`}, "", false},
		{SampleRow{SampleID: "sha256:quarantined", ManifestJSON: `{"packages":["pkg:npm/axios@1.0.0"],"symbols":["axios.put"]}`, Quarantined: true}, "PASS", true},
		{SampleRow{SampleID: "sha256:other-package", ManifestJSON: `{"packages":["pkg:npm/axios-retry@1.0.0"],"symbols":["axiosRetry"]}`}, "PASS", true},
	}
	for _, sample := range samples {
		if err := store.SaveSample(ctx, sample.row); err != nil {
			t.Fatalf("save sample %s: %v", sample.row.SampleID, err)
		}
		if !sample.receipt {
			continue
		}
		if err := store.SaveReceipt(ctx, ReceiptRow{
			ReceiptID:      sample.row.SampleID + "-receipt",
			SampleID:       sample.row.SampleID,
			PeerID:         "ed25519:code-count-peer",
			ReceiptJSON:    `{}`,
			ContractResult: sample.verdict,
		}); err != nil {
			t.Fatalf("save receipt %s: %v", sample.row.SampleID, err)
		}
	}
}

func assertVerifiedSampleCodeCounts(t *testing.T, store verifiedSampleCodeCountStore) {
	t.Helper()
	seedVerifiedSampleCodeCounts(t, store)
	got, err := store.VerifiedSampleCodeCounts(context.Background(), "pkg:npm/axios@")
	if err != nil {
		t.Fatal(err)
	}
	want := []VerifiedSampleCodeCount{
		{PURL: "pkg:npm/axios@1.0.0", Symbol: "", Samples: 2},
		{PURL: "pkg:npm/axios@1.0.0", Symbol: "axios.get", Samples: 2},
		{PURL: "pkg:npm/axios@1.0.0", Symbol: "axios.post", Samples: 1},
		{PURL: "pkg:npm/axios@2.0.0", Symbol: "", Samples: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verified code counts = %#v, want %#v", got, want)
	}
}

func TestVerifiedSampleCodeCountsAreExhaustiveAndExactInFake(t *testing.T) {
	assertVerifiedSampleCodeCounts(t, NewFake())
}

func TestIntegrationVerifiedSampleCodeCountsMatchFake(t *testing.T) {
	assertVerifiedSampleCodeCounts(t, openTestPG(t))
}
