package httpapi

import (
	"net/http"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A client stops retrying only when the server says a refusal is final, so
// what the server calls final IS the contract.
//
// PRIVATE is the registry's decision and asking again will not change it.
// UNKNOWN is this server declining to store what it could not check — the
// per-request lookup budget ran out, or no checker is configured — and a
// client that treated that as final would discard evidence about a public
// package nobody had got around to confirming. Production is exactly where
// that happens: the budget is per request and the backlog was thousands of
// batches deep, which is how 852 refusals pinned a 1,000-item queue.
func TestOnlyADecidedRefusalIsTerminal(t *testing.T) {
	for _, tc := range []struct {
		name         string
		verdict      string
		wantCode     string
		wantTerminal bool
	}{
		{"the registry says private", scanner.PublicnessPrivate, serverstore.RejectNotPublic, true},
		{"the server could not check", scanner.PublicnessUnknown, serverstore.RejectPublicnessUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const purl = "pkg:npm/inhouse-thing@1.0.0"
			srv, _ := dependencyServer(t, &scriptedRegistry{
				verdicts: map[string]string{purl: tc.verdict},
			})
			batches := map[string]any{"batches": []domain.ObservationBatch{
				testBatch(purl, "thing.run", nodeEnv("esm"),
					domain.StageProjectCompile, domain.ResultPass, 3),
			}}
			var out ingestResponse
			resp := postJSON(t, srv.URL+"/v1/evidence/batches", batches, &out)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", resp.StatusCode)
			}
			if len(out.Rejected) != 1 {
				t.Fatalf("rejected = %+v, want one refusal", out.Rejected)
			}
			got := out.Rejected[0]
			if got.Code != tc.wantCode || got.Terminal != tc.wantTerminal {
				t.Errorf("code=%q terminal=%v, want %q/%v (reason %q)",
					got.Code, got.Terminal, tc.wantCode, tc.wantTerminal, got.Reason)
			}
		})
	}
}

// A batch that cannot satisfy the contract is terminal: the same bytes do not
// become valid by waiting.
func TestAnInvalidBatchIsTerminal(t *testing.T) {
	srv, _ := dependencyServer(t, &scriptedRegistry{})
	bad := testBatch("pkg:npm/axios@1.12.0", "axios.post", nodeEnv("esm"),
		domain.StageProjectCompile, domain.ResultPass, 3)
	bad.SchemaVersion = 99 // no such contract version

	var out ingestResponse
	postJSON(t, srv.URL+"/v1/evidence/batches", map[string]any{
		"batches": []domain.ObservationBatch{bad}}, &out)
	if len(out.Rejected) != 1 {
		t.Fatalf("rejected = %+v, want one refusal", out.Rejected)
	}
	if got := out.Rejected[0]; got.Code != serverstore.RejectInvalidBatch || !got.Terminal {
		t.Errorf("code=%q terminal=%v, want %q/true", got.Code, got.Terminal, serverstore.RejectInvalidBatch)
	}
}
