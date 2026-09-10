package compatibility

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type receiptPageRecordingStore struct {
	*serverstore.Fake
	batches     [][]string
	singleCalls int
}

func (s *receiptPageRecordingStore) ReceiptsForSamples(ctx context.Context, sampleIDs []string) (map[string][]serverstore.ReceiptRow, error) {
	s.batches = append(s.batches, append([]string(nil), sampleIDs...))
	out := make(map[string][]serverstore.ReceiptRow, len(sampleIDs))
	for _, sampleID := range sampleIDs {
		rows, err := s.Fake.ReceiptsForSample(ctx, sampleID)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			out[sampleID] = rows
		}
	}
	return out, nil
}

func (s *receiptPageRecordingStore) ReceiptsForSample(context.Context, string) ([]serverstore.ReceiptRow, error) {
	s.singleCalls++
	return nil, fmt.Errorf("unexpected single-sample receipt read")
}

func TestBuilderLoadsReceiptsOncePerSamplePageWithoutChangingEvidenceInputs(t *testing.T) {
	fake := serverstore.NewFake()
	store := &receiptPageRecordingStore{Fake: fake}
	ctx := context.Background()

	// This fixture carries real receipt-derived evidence. The remaining rows
	// take the corpus over a page boundary so the same test pins both parity
	// and scale: one bounded receipt read per page, never one per sample.
	seedBuilderFixture(t, fake)
	for i := 0; i < loadSampleBatch; i++ {
		row := serverstore.SampleRow{
			SampleID:     fmt.Sprintf("sha256:%064x", i+1),
			ManifestJSON: `{}`,
		}
		if err := fake.SaveSample(ctx, row); err != nil {
			t.Fatalf("SaveSample %d: %v", i, err)
		}
	}

	want, err := (&Builder{Store: fake}).loadSamples(ctx)
	if err != nil {
		t.Fatalf("fallback loadSamples: %v", err)
	}
	got, err := (&Builder{Store: store}).loadSamples(ctx)
	if err != nil {
		t.Fatalf("bulk loadSamples: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("bulk receipt pages changed the sample/receipt evidence supplied to snapshots, regressions, clusters, or shards")
	}
	if got, want := len(got), loadSampleBatch+1; got != want {
		t.Fatalf("loaded samples = %d, want %d", got, want)
	}
	if got, want := len(store.batches), 2; got != want {
		t.Fatalf("receipt batch calls = %d, want %d", got, want)
	}
	if got, want := len(store.batches[0]), loadSampleBatch; got != want {
		t.Fatalf("first receipt batch size = %d, want %d", got, want)
	}
	if got, want := len(store.batches[1]), 1; got != want {
		t.Fatalf("second receipt batch size = %d, want %d", got, want)
	}
	if store.singleCalls != 0 {
		t.Fatalf("single-sample receipt calls = %d, want 0", store.singleCalls)
	}
}
