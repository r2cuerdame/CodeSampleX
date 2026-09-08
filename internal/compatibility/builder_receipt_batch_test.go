package compatibility

import (
	"context"
	"fmt"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type receiptPageRecordingStore struct {
	*serverstore.Fake
	batches [][]string
}

func (s *receiptPageRecordingStore) ReceiptsForSamples(_ context.Context, sampleIDs []string) (map[string][]serverstore.ReceiptRow, error) {
	s.batches = append(s.batches, append([]string(nil), sampleIDs...))
	return map[string][]serverstore.ReceiptRow{}, nil
}

func TestBuilderLoadsReceiptsOncePerSamplePage(t *testing.T) {
	fake := serverstore.NewFake()
	store := &receiptPageRecordingStore{Fake: fake}
	ctx := context.Background()

	for i := 0; i < loadSampleBatch+1; i++ {
		row := serverstore.SampleRow{
			SampleID:     fmt.Sprintf("sha256:%064x", i+1),
			ManifestJSON: `{}`,
		}
		if err := fake.SaveSample(ctx, row); err != nil {
			t.Fatalf("SaveSample %d: %v", i, err)
		}
	}

	builder := &Builder{Store: store}
	rows, err := builder.loadSamples(ctx)
	if err != nil {
		t.Fatalf("loadSamples: %v", err)
	}
	if got, want := len(rows), loadSampleBatch+1; got != want {
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
}
