package serverstore

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func runJobsForSampleOrderingContract(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	const sampleID = "sha256:jobs-for-sample-order"
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	// Insert newest first. The two oldest jobs share a timestamp so their IDs
	// must determine their order independently of their insertion position.
	want := []JobRow{
		{SampleID: sampleID, Reason: "cross", Status: "open", CreatedAt: base},
		{SampleID: sampleID, Reason: "matrix", Status: "done", CreatedAt: base},
		{SampleID: sampleID, Reason: "cross", Status: "claimed", CreatedAt: base.Add(time.Hour)},
	}
	ids := make([]int64, len(want))
	for i := len(want) - 1; i >= 0; i-- {
		id, err := store.CreateJob(ctx, want[i])
		if err != nil {
			t.Fatalf("CreateJob[%d]: %v", i, err)
		}
		ids[i] = id
		want[i].ID = id
	}

	got, err := store.JobsForSample(ctx, sampleID)
	if err != nil {
		t.Fatalf("JobsForSample: %v", err)
	}
	// Creation IDs follow insertion order, so explicitly arrange expected
	// oldest-first order with ID ascending for the equal-time pair.
	expected := []JobRow{want[1], want[0], want[2]}
	if ids[1] > ids[0] {
		expected[0], expected[1] = expected[1], expected[0]
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("JobsForSample order = %+v, want %+v", got, expected)
	}
}

func TestJobsForSampleOrderingParity(t *testing.T) {
	runJobsForSampleOrderingContract(t, NewFake())
}

func TestIntegrationPGJobsForSampleOrderingParity(t *testing.T) {
	pg := openTestPG(t)
	t.Cleanup(pg.Close)
	runJobsForSampleOrderingContract(t, pg)
}
