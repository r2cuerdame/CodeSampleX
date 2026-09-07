package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/httpapi"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type startupWantedStore struct {
	serverstore.Store
	entered chan struct{}
	release chan struct{}
	err     error
}

func (s *startupWantedStore) TopWanted(ctx context.Context, _ int) ([]serverstore.WantedRow, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	return []serverstore.WantedRow{{Ecosystem: "npm", Name: "primed", Version: "1.0.0"}}, nil
}

// TestBuildMuxMountsV1API proves BuildMux serves the real /v1 API over a
// Store: adapters matrix, evidence ingest (trust mode), and stats.
func TestBuildMuxMountsV1API(t *testing.T) {
	store := serverstore.NewFake()
	cfg := serverstore.ServerConfig{
		PublicCheck: "trust",
		BlobDir:     t.TempDir(),
	}
	srv := httptest.NewServer(BuildMux(cfg, store))
	defer srv.Close()

	// Adapters matrix served from the embedded copy.
	resp, err := http.Get(srv.URL + "/v1/adapters")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adapters status = %d", resp.StatusCode)
	}
	canonical, err := os.ReadFile("../../schemas/v1/adapters.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, canonical) {
		t.Fatal("adapters body differs from schemas/v1/adapters.json")
	}

	// Evidence ingest in trust mode.
	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-13", AnonID: "peer1", ProjectBucket: "proj1",
		Package: "pkg:npm/axios@1.12.0",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
	}
	payload, _ := json.Marshal(map[string]any{"batches": []domain.ObservationBatch{batch}})
	resp, err = http.Post(srv.URL+"/v1/evidence/batches", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest status = %d, want 202", resp.StatusCode)
	}

	// Stats endpoint answers even before the first builder pass and always
	// labels estimates.
	resp, err = http.Get(srv.URL + "/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	statsBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d", resp.StatusCode)
	}
	if !bytes.Contains(statsBody, []byte(`"estimated":true`)) {
		t.Fatalf("stats missing estimated flag: %s", statsBody)
	}
}

// TestStartBuilderRunsPipeline proves the serve seam actually produces
// materialized outputs on the snapshot interval.
func TestStartBuilderRunsPipeline(t *testing.T) {
	store := serverstore.NewFake()
	_, _, err := store.IngestBatches(context.Background(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-13", AnonID: "peer1", ProjectBucket: "proj1",
		Package: "pkg:npm/axios@1.12.0",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 3,
	}})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartBuilder(ctx, serverstore.ServerConfig{SnapshotInterval: time.Minute}, store)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok, _ := store.GetSnapshot(context.Background(), "pkg:npm/axios@1.12.0", ""); ok {
			if _, _, ok, _ := store.GetShard(context.Background(), "npm/axios/1"); ok {
				return // pipeline materialized snapshot + shard
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("builder did not materialize snapshot/shard in time")
}

func TestWantedSnapshotIsPrimedBeforeBuilderStarts(t *testing.T) {
	store := &startupWantedStore{
		Store:   serverstore.NewFake(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	started := make(chan struct{})
	type result struct {
		snapshot *httpapi.WantedSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() {
		snapshot, err := primeWantedBeforeBuilder(context.Background(), serverstore.ServerConfig{}, store,
			func(context.Context, serverstore.ServerConfig, serverstore.Store) { close(started) })
		done <- result{snapshot: snapshot, err: err}
	}()

	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("wanted preload did not start")
	}
	select {
	case <-started:
		t.Fatal("builder started before wanted preload completed")
	default:
	}
	close(store.release)
	got := <-done
	if got.err != nil || got.snapshot == nil || len(got.snapshot.Rows) != 1 {
		t.Fatalf("prime result snapshot=%+v err=%v", got.snapshot, got.err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("builder did not start after wanted preload")
	}
}

func TestWantedSnapshotFailurePreventsBuilderStart(t *testing.T) {
	wantErr := errors.New("wanted unavailable")
	store := &startupWantedStore{
		Store:   serverstore.NewFake(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
		err:     wantErr,
	}
	close(store.release)
	started := false
	snapshot, err := primeWantedBeforeBuilder(context.Background(), serverstore.ServerConfig{}, store,
		func(context.Context, serverstore.ServerConfig, serverstore.Store) { started = true })
	if !errors.Is(err, wantErr) || snapshot != nil {
		t.Fatalf("prime result snapshot=%+v err=%v, want %v", snapshot, err, wantErr)
	}
	if started {
		t.Fatal("builder started after wanted preload failed")
	}
}
