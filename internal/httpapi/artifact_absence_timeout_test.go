package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type sampleTimeoutStore struct {
	*serverstore.Fake
	getSampleErr error
}

func (s *sampleTimeoutStore) GetSample(ctx context.Context, sampleID string) (serverstore.SampleRow, bool, error) {
	if s.getSampleErr != nil {
		return serverstore.SampleRow{}, false, s.getSampleErr
	}
	return s.Fake.GetSample(ctx, sampleID)
}

func pgTimeoutErr() error {
	return &pgconn.PgError{
		Code:    "57014",
		Message: "canceling statement due to statement timeout",
	}
}

// 1. Proven absent sample artifact returns 404.
func TestSampleArtifact_ProvenAbsentReturns404(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	const sampleID = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	resp, err := http.Get(srv.URL + "/v1/samples/" + sampleID + "/artifact")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for proven absent sample artifact, got %d", resp.StatusCode)
	}
}

// 2. Store query timeout on artifact read returns 503, NEVER 404.
func TestSampleArtifact_StoreTimeoutReturns503Never404(t *testing.T) {
	const sampleID = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Store = &sampleTimeoutStore{
			Fake:         d.Store.(*serverstore.Fake),
			getSampleErr: pgTimeoutErr(),
		}
	})

	resp, err := http.Get(srv.URL + "/v1/samples/" + sampleID + "/artifact")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("CRITICAL DEFECT: Store timeout returned 404 Not Found for sample artifact!")
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable, got %d", resp.StatusCode)
	}

	// Verify headers: Retry-After and negative-cache prevention
	if ra := resp.Header.Get("Retry-After"); ra == "" {
		t.Errorf("expected Retry-After header on 503")
	}
	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "no-cache") || !strings.Contains(cc, "no-store") {
		t.Errorf("expected Cache-Control: no-cache, no-store on 503, got %q", cc)
	}
}

// 3. Context deadline exceeded returns 504 Gateway Timeout, NEVER 404.
func TestSampleArtifact_ContextDeadlineExceededReturns504Never404(t *testing.T) {
	const sampleID = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Store = &sampleTimeoutStore{
			Fake:         d.Store.(*serverstore.Fake),
			getSampleErr: context.DeadlineExceeded,
		}
	})

	resp, err := http.Get(srv.URL + "/v1/samples/" + sampleID + "/artifact")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("CRITICAL DEFECT: DeadlineExceeded returned 404 Not Found for sample artifact!")
	}
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 Gateway Timeout, got %d", resp.StatusCode)
	}
	if ra := resp.Header.Get("Retry-After"); ra == "" {
		t.Errorf("expected Retry-After header on 504")
	}
	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "no-cache") || !strings.Contains(cc, "no-store") {
		t.Errorf("expected Cache-Control: no-cache, no-store on 504, got %q", cc)
	}
}

// 4. Sample detail endpoint under store timeout returns 503, NEVER 404.
func TestSampleDetail_StoreTimeoutReturns503Never404(t *testing.T) {
	const sampleID = "sha256:3333333333333333333333333333333333333333333333333333333333333333"

	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Store = &sampleTimeoutStore{
			Fake:         d.Store.(*serverstore.Fake),
			getSampleErr: pgTimeoutErr(),
		}
	})

	resp, err := http.Get(srv.URL + "/v1/samples/" + sampleID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("CRITICAL DEFECT: Store timeout returned 404 Not Found for sample detail!")
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable, got %d", resp.StatusCode)
	}
}

// 5. Sample peers endpoint under store timeout returns 503, NEVER 500 or 404.
func TestSamplePeers_StoreTimeoutReturns503Never404(t *testing.T) {
	const sampleID = "sha256:4444444444444444444444444444444444444444444444444444444444444444"

	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Store = &sampleTimeoutStore{
			Fake:         d.Store.(*serverstore.Fake),
			getSampleErr: pgTimeoutErr(),
		}
	})

	resp, err := http.Get(srv.URL + "/v1/peers/for-sample/" + sampleID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("CRITICAL DEFECT: Store timeout returned %d for sample peers!", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable, got %d", resp.StatusCode)
	}
}
