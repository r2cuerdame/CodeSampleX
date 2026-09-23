package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Regression tests for the server symptoms consolidated under #483.

func strictPublicCheck(d *Deps) {
	d.Cfg.PublicCheck = "strict"
	d.Checker = &fakePublicChecker{verdict: map[string]string{
		"pkg:npm/axios@1.12.0": scanner.PublicnessPublic,
	}}
}

const notPublicRefusal = "not a confirmed public coordinate"

// Under the production strict check, a fixed public target (a CLI tool, an
// engine) is reportable; an arbitrary generic name is not (#349).
func TestAnomalyReportsAcceptFixedPublicTargetsUnderStrictCheck(t *testing.T) {
	srv, store, _ := newTestServer(t, strictPublicCheck)
	sampleID := saveSampleForVerification(t, store, "d4")

	for _, purl := range []string{"pkg:generic/cli/git@2.39.0", "pkg:generic/engine/unreal@5.5", "pkg:npm/axios@1.12.0"} {
		report := mismatchAgainst(sampleID)
		report.Package = purl
		report.Symbol = ""
		var body map[string]any
		resp := postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &body)
		if resp.StatusCode == http.StatusBadRequest {
			t.Errorf("%s refused under strict check: %v", purl, body["error"])
		}
	}

	for _, purl := range []string{"pkg:generic/cli/internal-secret-tool@1.0.0", "pkg:generic/unknown/tool@1.0.0"} {
		report := mismatchAgainst(sampleID)
		report.Package = purl
		report.Symbol = ""
		var body map[string]any
		resp := postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body["error"].(string), notPublicRefusal) {
			t.Errorf("%s: status %d body %v, want the not-public refusal", purl, resp.StatusCode, body)
		}
	}
}

func TestCSXIssueReportsAcceptFixedPublicTargetsUnderStrictCheck(t *testing.T) {
	srv, _, _ := newTestServer(t, strictPublicCheck)
	for _, c := range []struct {
		purl   string
		public bool
	}{
		{"pkg:generic/cli/docker@24.0.0", true},
		{"pkg:generic/cli/internal-secret-tool@1.0.0", false},
	} {
		report := gptBrowserIssue()
		report.PublicInput = &domain.CSXIssuePublicInput{Endpoint: "/v2/search", Packages: []string{c.purl}}
		var body map[string]any
		resp := postJSON(t, srv.URL+"/v1/csx-issues", csxIssueEnvelopeFor(report), &body)
		refused := resp.StatusCode == http.StatusBadRequest && strings.Contains(body["error"].(string), notPublicRefusal)
		if refused == c.public {
			t.Errorf("%s: status %d body %v, public=%v", c.purl, resp.StatusCode, body, c.public)
		}
	}
}

// A CLI execution evidence id is a CSX content id, not token material (#359).
func TestAnomalyReportsAcceptCLIExecutionEvidenceIDs(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveSampleForVerification(t, store, "e5")
	hash := strings.Repeat("0123456789abcdef", 4)

	report := mismatchAgainst(sampleID)
	report.EvidenceID = "clievidence:sha256:" + hash
	report.RelatedIDs = []string{"cliobs:sha256:" + hash, "clievidence:sha256:" + hash}
	var body map[string]any
	resp := postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body %v, want 200", resp.StatusCode, body)
	}

	for _, bad := range []string{"clievidence:/home/me/secret", "note:ghp_" + hash} {
		report := mismatchAgainst(sampleID)
		report.RelatedIDs = []string{bad}
		resp := postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("related id %q: status %d, want 400", bad, resp.StatusCode)
		}
	}
	if anomalyIdentifierIsSafe("anything:sha256:" + hash) {
		t.Error("an unknown namespace in front of a hash was treated as a CSX id")
	}
}

// The reconciler settles a fixed public target PUBLIC, not PRIVATE (#312).
func TestReconcileUncheckedPublicnessKeepsFixedPublicTargetsPublic(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	rows := []serverstore.PackageRow{
		{PURL: "pkg:generic/cli/git@2.43.0", Ecosystem: "generic", Name: "cli/git", Version: "2.43.0", Major: "2"},
		{PURL: "pkg:generic/engine/unreal@5.5", Ecosystem: "generic", Name: "engine/unreal", Version: "5.5", Major: "5"},
		{PURL: "pkg:generic/cli/internal-secret-tool@1.0.0", Ecosystem: "generic", Name: "cli/internal-secret-tool", Version: "1.0.0", Major: "1"},
	}
	for _, r := range rows {
		r.Publicness = scanner.PublicnessUnknown
		if err := store.UpsertPackage(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReconcileUncheckedPublicness(ctx, store, &fakePublicChecker{}, 100); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"pkg:generic/cli/git@2.43.0":                 scanner.PublicnessPublic,
		"pkg:generic/engine/unreal@5.5":              scanner.PublicnessPublic,
		"pkg:generic/cli/internal-secret-tool@1.0.0": scanner.PublicnessPrivate,
	}
	for purl, publicness := range want {
		got, ok, err := store.GetPackage(ctx, purl)
		if err != nil || !ok {
			t.Fatalf("%s missing: %v", purl, err)
		}
		if got.Publicness != publicness || got.CheckedAt.IsZero() {
			t.Errorf("%s settled %s (checked %v), want %s", purl, got.Publicness, got.CheckedAt, publicness)
		}
	}
}
