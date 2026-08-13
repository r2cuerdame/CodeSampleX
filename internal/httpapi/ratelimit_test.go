package httpapi

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// TestRateLimitThrottlesAndRefills pins the whole point of the limiter: a
// client past its budget is told so with 429 + Retry-After (never silently
// dropped, which just makes it retry harder), and gets its allowance back
// as time passes.
func TestRateLimitThrottlesAndRefills(t *testing.T) {
	clock := testNow
	small := &limiter{buckets: map[string]*bucket{}, rate: rate{burst: 3, per: time.Minute},
		now: func() time.Time { return clock }}
	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Limits = &limiters{write: small, read: newLimiter(readLimit), auth: newLimiter(authLimit)}
	})

	announce := map[string]any{"peerId": "ed25519:" + "ab12cd34ef567890", "port": 48620}
	for i := 0; i < 3; i++ {
		if resp := postJSON(t, srv.URL+"/v1/peers/announce", announce, nil); resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("request %d throttled inside the burst", i+1)
		}
	}

	resp := postJSON(t, srv.URL+"/v1/peers/announce", announce, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 past the burst", resp.StatusCode)
	}
	retry, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || retry < 1 {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", resp.Header.Get("Retry-After"))
	}

	// Tokens refill with time rather than needing a background sweeper.
	clock = clock.Add(time.Minute)
	if resp := postJSON(t, srv.URL+"/v1/peers/announce", announce, nil); resp.StatusCode == http.StatusTooManyRequests {
		t.Fatal("bucket did not refill after a full period")
	}
}

// TestRateLimitIsPerClient: one abusive client must not throttle everyone
// else, which is the difference between a rate limit and an outage.
func TestRateLimitIsPerClient(t *testing.T) {
	l := &limiter{buckets: map[string]*bucket{}, rate: rate{burst: 2, per: time.Minute},
		now: func() time.Time { return testNow }}

	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("203.0.113.7"); !ok {
			t.Fatalf("noisy client throttled inside its own burst at %d", i)
		}
	}
	if ok, _ := l.allow("203.0.113.7"); ok {
		t.Fatal("noisy client was not throttled past its burst")
	}
	if ok, _ := l.allow("198.51.100.4"); !ok {
		t.Fatal("a second client was throttled by the first client's usage")
	}
}

// TestRateLimitEvictsIdleBuckets: the limiter defends against exhaustion,
// so a flood of unique keys must not grow its own map without bound.
func TestRateLimitEvictsIdleBuckets(t *testing.T) {
	clock := testNow
	l := &limiter{buckets: map[string]*bucket{}, rate: rate{burst: 5, per: time.Minute},
		now: func() time.Time { return clock }}

	for i := 0; i < 500; i++ {
		l.allow("10.0.0." + strconv.Itoa(i))
	}
	if len(l.buckets) != 500 {
		t.Fatalf("buckets = %d, want 500 before any sweep", len(l.buckets))
	}

	// Every bucket is now idle past the threshold; the next call sweeps.
	clock = clock.Add(sweepIdleAfter + time.Minute)
	l.allow("10.0.0.1")
	if len(l.buckets) != 1 {
		t.Fatalf("buckets = %d after sweep, want only the live one", len(l.buckets))
	}
}

// TestHealthzIsNotRateLimited: throttling the health check would take the
// deployment out of service on its own.
func TestHealthzIsNotRateLimited(t *testing.T) {
	tiny := &limiter{buckets: map[string]*bucket{}, rate: rate{burst: 1, per: time.Hour},
		now: func() time.Time { return testNow }}
	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Limits = &limiters{write: tiny, read: tiny, auth: tiny}
	})
	for i := 0; i < 5; i++ {
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("healthz #%d status = %d, want 200", i+1, resp.StatusCode)
		}
	}
}

// TestSampleUploadRespectsBlobBudget: sample upload is anonymous, so the
// blob budget is the only thing standing between a script and a full disk
// — and PostgreSQL shares that volume.
func TestSampleUploadRespectsBlobBudget(t *testing.T) {
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, map[string]string{"src/echo.mjs": "export const x = 1;\n"})
	sampleID := domain.SHA256Hex(artifact)

	// Budget of 1 byte: already exceeded once anything is stored, and even
	// an empty store is >= 1 only after the first write, so the first
	// upload succeeds and the second distinct one is refused.
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Cfg.BlobBudgetBytes = 1 })

	if resp := postSample(t, srv.URL, manifest, sampleID, artifact, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first upload status = %d, want 201", resp.StatusCode)
	}

	// Same content again: a content-addressed no-op must still succeed, or
	// a re-publish would start failing the moment the budget is reached.
	if resp := postSample(t, srv.URL, manifest, sampleID, artifact, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-upload of stored content status = %d, want 201", resp.StatusCode)
	}

	other := manifest
	other.Case.Goal = "a different goal entirely"
	otherArtifact := buildArtifact(t, other, map[string]string{"src/echo.mjs": "export const y = 2;\n"})
	resp := postSample(t, srv.URL, other, domain.SHA256Hex(otherArtifact), otherArtifact, "")
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("new artifact past budget status = %d, want 507", resp.StatusCode)
	}
}
