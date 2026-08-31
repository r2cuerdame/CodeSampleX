package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// countingChecker records how many publicness lookups reach the network.
type countingChecker struct{ n atomic.Int32 }

func (c *countingChecker) Check(_ context.Context, _ domain.PURL) string {
	c.n.Add(1)
	return scanner.PublicnessPublic
}

// The publicness check hits a third-party registry with a package name the
// CALLER chose, and it ran once per batch — so one anonymous request could
// fire hundreds of sequential probes at npmjs.org or pypi.org for names
// nobody has published, since only an uncached name reaches the network.
// That points this server at somebody else.
func TestOneRequestCannotFanOutToTheRegistry(t *testing.T) {
	c := &countingChecker{}
	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Checker = c
		d.Cfg.PublicCheck = "registry" // trust mode skips the check entirely
	})

	batches := make([]domain.ObservationBatch, 0, 200)
	for i := 0; i < 200; i++ {
		batches = append(batches, testBatch(
			fmt.Sprintf("pkg:npm/nope-%d@1.0.0", i), "nope.call", nodeEnv("esm"),
			domain.StageProjectCompile, domain.ResultPass, 1))
	}
	var out ingestResponse
	resp := postJSON(t, srv.URL+"/v1/evidence/batches",
		map[string]any{"batches": batches}, &out)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := int(c.n.Load()); got > maxRegistryLookupsPerRequest {
		t.Errorf("one request caused %d outbound lookups; the cap is %d",
			got, maxRegistryLookupsPerRequest)
	}
}

// An honest client sending many batches about a few packages must not be
// penalised: a repeated package is answered from the request's own memo.
func TestRepeatedPackagesCostOneLookup(t *testing.T) {
	c := &countingChecker{}
	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Checker = c
		d.Cfg.PublicCheck = "registry" // trust mode skips the check entirely
	})

	batches := make([]domain.ObservationBatch, 0, 50)
	for i := 0; i < 50; i++ {
		batches = append(batches, testBatch(
			"pkg:npm/axios@1.12.0", "axios.post", nodeEnv("esm"),
			domain.StageProjectCompile, domain.ResultPass, 1))
	}
	var out ingestResponse
	postJSON(t, srv.URL+"/v1/evidence/batches",
		map[string]any{"batches": batches}, &out)

	if got := int(c.n.Load()); got != 1 {
		t.Errorf("50 batches for one package caused %d lookups, want 1", got)
	}
}

// cachedChecker answers some packages from a cache without reaching the
// network, exactly as registry.Checker does for an entry younger than its TTL.
type cachedChecker struct {
	cached map[string]string
	n      atomic.Int32
}

func (c *cachedChecker) CachedPublicness(_ context.Context, p domain.PURL) (string, bool) {
	status, ok := c.cached[p.String()]
	return status, ok
}

func (c *cachedChecker) Check(ctx context.Context, p domain.PURL) string {
	if status, ok := c.CachedPublicness(ctx, p); ok {
		return status
	}
	c.n.Add(1)
	return scanner.PublicnessPublic
}

// A package already known to be public must not spend the registry budget.
//
// The cap exists to bound how many NEW names one request can push at a
// third-party registry. It was charged for every answer instead, including
// the ones that never left the process — so a request whose batches cluster
// on packages the server already knows burned the whole budget on cache hits,
// and every package after the twentieth came back UNKNOWN.
//
// That is refused as `publicness-unknown`, which is retryable by design and
// correctly so: UNKNOWN means the server could not check, not that the package
// is private. The batches therefore return on the next sync and are refused
// the same way forever. Production on 2026-08-31: 226 packages never checked
// once, the oldest first seen on 08-17, and a farm daemon refused 826, 890,
// 976, 916 and 920 batches on five consecutive cycles without the number
// falling. The queue sits against its 1,000 cap and new evidence has nowhere
// to go (#106).
func TestACachedAnswerDoesNotSpendTheRegistryBudget(t *testing.T) {
	c := &cachedChecker{cached: map[string]string{}}
	batches := make([]domain.ObservationBatch, 0, maxRegistryLookupsPerRequest+1)
	// Every one of these is already known and costs no outbound call.
	for i := 0; i < maxRegistryLookupsPerRequest+5; i++ {
		purl := fmt.Sprintf("pkg:npm/known-%d@1.0.0", i)
		c.cached[purl] = scanner.PublicnessPublic
		batches = append(batches, testBatch(purl, "known.call", nodeEnv("esm"),
			domain.StageProjectCompile, domain.ResultPass, 1))
	}
	// And one the server has never seen, which is what the budget is for.
	batches = append(batches, testBatch("pkg:npm/never-checked@1.0.0", "never.call",
		nodeEnv("esm"), domain.StageProjectCompile, domain.ResultPass, 1))

	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Checker = c
		d.Cfg.PublicCheck = "registry"
	})
	var out ingestResponse
	resp := postJSON(t, srv.URL+"/v1/evidence/batches",
		map[string]any{"batches": batches}, &out)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := int(c.n.Load()); got != 1 {
		t.Errorf("%d outbound lookups, want 1: cached answers were charged to the budget", got)
	}
	for _, rej := range out.Rejected {
		t.Errorf("batch %d refused: %s (%s)", rej.Index, rej.Reason, rej.Code)
	}
}
