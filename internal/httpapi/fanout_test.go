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
