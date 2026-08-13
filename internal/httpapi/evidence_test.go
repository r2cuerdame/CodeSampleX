package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type ingestResponse struct {
	Accepted int                         `json:"accepted"`
	Rejected []serverstore.RejectedBatch `json:"rejected"`
}

func TestIngestAcceptsValidBatch(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)

	batches := map[string]any{"batches": []domain.ObservationBatch{
		testBatch("pkg:npm/axios@1.12.0", "axios.post", nodeEnv("esm"),
			domain.StageProjectCompile, domain.ResultPass, 10),
	}}
	var out ingestResponse
	resp := postJSON(t, srv.URL+"/v1/evidence/batches", batches, &out)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if out.Accepted != 1 || len(out.Rejected) != 0 {
		t.Fatalf("resp = %+v", out)
	}
	rows, err := store.EvidenceForTarget(context.Background(), "pkg:npm/axios@1.12.0", "axios.post")
	if err != nil || len(rows) != 1 || rows[0].ObservationCount != 10 {
		t.Fatalf("evidence rows = %+v err=%v", rows, err)
	}
}

func TestIngestDuplicateBatchDoesNotInflate(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)

	batches := map[string]any{"batches": []domain.ObservationBatch{
		testBatch("pkg:npm/axios@1.12.0", "", nodeEnv("esm"),
			domain.StageProjectCompile, domain.ResultPass, 10),
	}}
	postJSON(t, srv.URL+"/v1/evidence/batches", batches, nil)
	postJSON(t, srv.URL+"/v1/evidence/batches", batches, nil) // re-send

	rows, _ := store.EvidenceForTarget(context.Background(), "pkg:npm/axios@1.12.0", "")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].ObservationCount != 10 {
		t.Fatalf("count = %d, want 10 (identical re-send adds 0)", rows[0].ObservationCount)
	}
	if rows[0].UniquePeerBuckets != 1 {
		t.Fatalf("uniquePeerBuckets = %d, want 1", rows[0].UniquePeerBuckets)
	}
}

func TestIngestRejectsInvalidAndOverLimit(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)

	// SYMBOL_EXECUTED is A3-only and must be rejected with its index.
	bad := testBatch("pkg:npm/axios@1.12.0", "axios.post", nodeEnv("esm"),
		domain.StageSymbolExecuted, domain.ResultPass, 1)
	good := testBatch("pkg:npm/axios@1.12.0", "", nodeEnv("esm"),
		domain.StageProjectCompile, domain.ResultPass, 1)
	var out ingestResponse
	resp := postJSON(t, srv.URL+"/v1/evidence/batches",
		map[string]any{"batches": []domain.ObservationBatch{bad, good}}, &out)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if out.Accepted != 1 || len(out.Rejected) != 1 || out.Rejected[0].Index != 0 {
		t.Fatalf("resp = %+v", out)
	}
	if !strings.Contains(out.Rejected[0].Reason, "A3") {
		t.Fatalf("reason = %q, want A3 mention", out.Rejected[0].Reason)
	}

	// 501 batches in one request: refused outright.
	many := make([]domain.ObservationBatch, 501)
	for i := range many {
		many[i] = good
	}
	resp = postJSON(t, srv.URL+"/v1/evidence/batches", map[string]any{"batches": many}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for >500 batches", resp.StatusCode)
	}
}

// TestIngestRejectsPrivatePackage runs the strict publicness gate against a
// fake npm registry: an unpublished package is PRIVATE and its batch is
// rejected with a reason; UNKNOWN would be rejected the same way.
func TestIngestRejectsPrivatePackage(t *testing.T) {
	fakeRegistry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "secret-internal") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeRegistry.Close()

	srv, store, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.PublicCheck = "strict"
		d.Checker = &registry.Checker{
			Cache:    &registry.ServerCache{Store: d.Store},
			HTTP:     fakeRegistry.Client(),
			BaseURLs: map[string]string{"npm": fakeRegistry.URL},
		}
	})

	private := testBatch("pkg:npm/secret-internal@1.0.0", "", nodeEnv("esm"),
		domain.StageProjectCompile, domain.ResultPass, 3)
	public := testBatch("pkg:npm/axios@1.12.0", "", nodeEnv("esm"),
		domain.StageProjectCompile, domain.ResultPass, 3)
	var out ingestResponse
	resp := postJSON(t, srv.URL+"/v1/evidence/batches",
		map[string]any{"batches": []domain.ObservationBatch{private, public}}, &out)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if out.Accepted != 1 || len(out.Rejected) != 1 || out.Rejected[0].Index != 0 {
		t.Fatalf("resp = %+v", out)
	}
	if !strings.Contains(out.Rejected[0].Reason, "not public") {
		t.Fatalf("reason = %q", out.Rejected[0].Reason)
	}
	// The private package left no evidence behind.
	rows, _ := store.EvidenceForTarget(context.Background(), "pkg:npm/secret-internal@1.0.0", "")
	if len(rows) != 0 {
		t.Fatalf("private package has %d evidence rows, want 0", len(rows))
	}
}

// --- registry reads ----------------------------------------------------------

func TestRegistryPackageAndSymbol(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	postJSON(t, srv.URL+"/v1/evidence/batches", map[string]any{
		"batches": []domain.ObservationBatch{
			testBatch("pkg:npm/axios@1.12.0", "axios.post", nodeEnv("esm"),
				domain.StageProjectCompile, domain.ResultPass, 10),
			testBatch("pkg:npm/axios@1.12.0", "", nodeEnv("esm"),
				domain.StageProjectCompile, domain.ResultPass, 10),
		},
	}, nil)
	builder := &compatibility.Builder{Store: store}
	if err := builder.RunOnce(context.Background()); err != nil {
		t.Fatalf("builder: %v", err)
	}

	var pkg struct {
		PURL       string   `json:"purl"`
		Publicness string   `json:"publicness"`
		Majors     []string `json:"majors"`
		Symbols    []string `json:"symbols"`
	}
	resp := getJSON(t, srv.URL+"/v1/registry/packages/pkg:npm%2Faxios@1.12.0", &pkg)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if pkg.PURL != "pkg:npm/axios@1.12.0" || len(pkg.Majors) != 1 || pkg.Majors[0] != "1" {
		t.Fatalf("pkg = %+v", pkg)
	}
	if len(pkg.Symbols) != 1 || pkg.Symbols[0] != "axios.post" {
		t.Fatalf("symbols = %v", pkg.Symbols)
	}

	var sym struct {
		Family    string `json:"family"`
		Snapshots []struct {
			PURL string `json:"purl"`
		} `json:"snapshots"`
	}
	resp = getJSON(t, srv.URL+"/v1/registry/symbols/npm/axios/axios.post", &sym)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("symbol status = %d, want 200", resp.StatusCode)
	}
	if sym.Family != "axios.post" || len(sym.Snapshots) != 1 {
		t.Fatalf("symbol = %+v", sym)
	}

	// Unknown package and symbol → 404 with JSON error.
	resp = getJSON(t, srv.URL+"/v1/registry/packages/pkg:npm%2Fnope@9.9.9", &struct{}{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown package status = %d, want 404", resp.StatusCode)
	}
	resp = getJSON(t, srv.URL+"/v1/registry/symbols/npm/axios/axios.nope", &struct{}{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown symbol status = %d, want 404", resp.StatusCode)
	}
}
