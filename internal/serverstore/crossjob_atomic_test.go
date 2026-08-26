package serverstore

import (
	"context"
	"sync"
	"testing"
)

func runEnsureCrossJobAtomicContract(t *testing.T, store Store) {
	t.Helper()
	const callers = 24
	ctx := context.Background()
	start := make(chan struct{})
	ids := make(chan int64, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, err := store.EnsureCrossJob(ctx, JobRow{
				SampleID: "sha256:atomic-cross-job", Reason: "cross",
				WantEnvJSON: `{"runtime":"node","runtimeVersion":"22"}`,
			})
			ids <- id
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)

	var wantID int64
	for err := range errs {
		if err != nil {
			t.Fatalf("EnsureCrossJob: %v", err)
		}
	}
	for id := range ids {
		if id == 0 {
			t.Fatal("EnsureCrossJob returned a zero id")
		}
		if wantID == 0 {
			wantID = id
		} else if id != wantID {
			t.Fatalf("concurrent callers got different jobs: %d and %d", wantID, id)
		}
	}
	jobs, err := store.JobsForSample(ctx, "sha256:atomic-cross-job")
	if err != nil {
		t.Fatal(err)
	}
	cross := 0
	for _, job := range jobs {
		if job.Reason == "cross" {
			cross++
		}
	}
	if cross != 1 {
		t.Fatalf("concurrent reuse created %d cross jobs, want 1", cross)
	}
}

func TestFakeEnsureCrossJobIsAtomic(t *testing.T) {
	runEnsureCrossJobAtomicContract(t, NewFake())
}

func TestIntegrationPGEnsureCrossJobIsAtomic(t *testing.T) {
	runEnsureCrossJobAtomicContract(t, openTestPG(t))
}
