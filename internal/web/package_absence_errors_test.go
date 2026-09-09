package web

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type packageAbsenceErrorStore struct {
	Store
	wantedErr   error
	clustersErr error
	codeCounts  []PackageCodeCount
}

func (s *packageAbsenceErrorStore) PackageCodeCounts(ctx context.Context, eco, name string) ([]PackageCodeCount, error) {
	if s.codeCounts != nil {
		return s.codeCounts, nil
	}
	return s.Store.PackageCodeCounts(ctx, eco, name)
}

func (s *packageAbsenceErrorStore) WantedForPackage(ctx context.Context, eco, name string) ([]WantedRow, error) {
	if s.wantedErr != nil {
		return nil, s.wantedErr
	}
	return s.Store.WantedForPackage(ctx, eco, name)
}

func (s *packageAbsenceErrorStore) FailureClusters(ctx context.Context, eco, name string) ([]string, int, error) {
	if s.clustersErr != nil {
		return nil, 0, s.clustersErr
	}
	return s.Store.FailureClusters(ctx, eco, name)
}

func TestPackageAbsenceRequiresSuccessfulWantedAndClusterReads(t *testing.T) {
	for _, source := range []string{"wanted", "clusters"} {
		t.Run(source, func(t *testing.T) {
			store := &packageAbsenceErrorStore{}
			mux, f := newTestMux(t, func(d *Deps) { store.Store = d.Store; d.Store = store })
			const name = "only-pending-evidence"
			const path = "/npm/" + name
			if source == "wanted" {
				f.wanted = []WantedRow{{Ecosystem: "npm", Name: name, Version: "1.0.0", Asks: 1}}
			} else {
				f.clusters["npm|"+name] = []string{`{"ecosystem":"npm","packageName":"only-pending-evidence","stage":"PROJECT_COMPILE","errorFp":"failure","observationCount":1}`}
			}
			if got := get(t, mux, path).Code; got != http.StatusOK {
				t.Fatalf("healthy evidence-only package status = %d, want 200", got)
			}
			if source == "wanted" {
				store.wantedErr = errors.New("database query timed out")
			} else {
				store.clustersErr = errors.New("database query timed out")
			}
			rec := get(t, mux, path)
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("unreadable %s status = %d, want 503, not false 404", source, rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != "2" {
				t.Errorf("Retry-After = %q, want 2", got)
			}
			// Older verified code can prove existence beyond the displayed sample window.
			store.codeCounts = []PackageCodeCount{{Version: "0.9.0", Samples: 1}}
			if got := get(t, mux, path).Code; got != http.StatusOK {
				t.Errorf("code-count-only package status = %d, want 200", got)
			}
			store.codeCounts = nil
			// A separate readable source still proves that an ordinary package exists.
			if got := get(t, mux, "/npm/axios").Code; got != http.StatusOK {
				t.Errorf("known package status = %d, want 200", got)
			}
			store.wantedErr, store.clustersErr = nil, nil
			if got := get(t, mux, path).Code; got != http.StatusOK {
				t.Errorf("recovered status = %d, want 200", got)
			}
			f.wanted = nil
			delete(f.clusters, "npm|"+name)
			if got := get(t, mux, path).Code; got != http.StatusNotFound {
				t.Errorf("proven absence status = %d, want 404", got)
			}
		})
	}
}
