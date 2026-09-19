package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func requestAuthoringWork(t *testing.T, serverURL, token string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, serverURL+"/v1/authoring/work/next",
		bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNonSampleGapsCompleteInsideCachedAuthoringSnapshot(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "evidence-only-writer", testNow)
	// An npm platform binary has no callable Sample surface, but the shipped
	// observer can still report its resolved presence. Maven pom-only packages
	// cannot exercise this delivery path: no ordinary-run Maven adapter ships.
	const purl = "pkg:npm/%40esbuild/win32-x64@0.25.0"
	if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "@esbuild/win32-x64",
		Version: "0.25.0", Publicness: "PUBLIC", LastSeen: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	first := requestAuthoringWork(t, srv.URL, token)
	work, _ := first["work"].(map[string]any)
	if first["status"] != "ASSIGNED" || work["axis"] != serverstore.AuthoringAxisDependency || work["package"] != purl {
		t.Fatalf("first poll = %#v, want Dependency assignment for a non-Sample coordinate", first)
	}

	accepted, rejected, err := store.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: testNow.Format("2006-01-02"), AnonID: "evidence-writer",
		ProjectBucket: "evidence-project", Package: purl, Direct: true,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22",
		},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1, DependsOnNone: true,
	}})
	if err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("evidence ingest: accepted=%d rejected=%v err=%v", accepted, rejected, err)
	}

	// The cached snapshot still contains the old Evidence and Sample rows.
	// The live predicate removes the completed Evidence/Dependency rows and the Sample N/A
	// rule removes the platform binary, so NO_WORK is now a justified answer.
	second := requestAuthoringWork(t, srv.URL, token)
	if second["status"] != "NO_WORK" {
		t.Fatalf("second poll = %#v, want justified NO_WORK", second)
	}
}

func TestSampleNotApplicableDoesNotEraseOtherCompletenessAxes(t *testing.T) {
	request := authoringWorkRequest{SandboxCapability: domain.CapContainerRun, VerifierOS: []string{"linux"}}
	base := serverstore.WantedRow{
		Ecosystem: "npm", Name: "@esbuild/win32-x64", Version: "0.25.0", Kind: "EXPANSION",
	}
	for _, tc := range []struct {
		axis string
		want bool
	}{
		{serverstore.AuthoringAxisSample, false},
		{serverstore.AuthoringAxisEvidence, true},
		{serverstore.AuthoringAxisDependency, true},
	} {
		candidate := base
		candidate.Axis = tc.axis
		if got := authoringCandidateEligible(candidate, request); got != tc.want {
			t.Errorf("axis %s eligible=%v, want %v", tc.axis, got, tc.want)
		}
	}
}

func TestNonSampleAxisRejectsSampleOnlyImpossibleOutcome(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "evidence-outcome-writer", testNow)
	work := serverstore.WantedRow{
		Ecosystem: "npm", Name: "evidence-outcome", Version: "1.0.0",
		Kind: "EXPANSION", Axis: serverstore.AuthoringAxisEvidence,
	}
	if _, found, err := store.ClaimAuthoringWork(t.Context(), "evidence-outcome-writer", []serverstore.WantedRow{work}, testNow, testNow.Add(time.Hour)); err != nil || !found {
		t.Fatalf("Evidence claim found=%v err=%v", found, err)
	}
	for _, outcome := range []string{"NO_CALLABLE_SYMBOL", "UNSUPPORTED_ENVIRONMENT"} {
		status, body := reportOutcome(t, srv.URL, token, `{"schemaVersion":1,"outcome":"`+outcome+`","detail":"not an Evidence conclusion"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%v, want 400", outcome, status, body)
		}
		if _, held, err := store.AuthoringWorkForSubmission(t.Context(), "evidence-outcome-writer", "", testNow); err != nil || !held {
			t.Fatalf("%s: rejected outcome released the claim: held=%v err=%v", outcome, held, err)
		}
	}
}

func TestEvidenceAuthoringRequiresAnOrdinaryRunObserver(t *testing.T) {
	request := authoringWorkRequest{SandboxCapability: domain.CapContainerRun, VerifierOS: []string{"linux"}, ClientVersion: "v0.1.168"}
	for _, eco := range []string{"pub", "maven", "composer", "gem", "hex"} {
		candidate := serverstore.WantedRow{Ecosystem: eco, Name: "ordinary-library", Version: "1.0.0", Kind: "EXPANSION", Axis: serverstore.AuthoringAxisEvidence}
		if authoringCandidateEligible(candidate, request) {
			t.Errorf("%s Evidence was offered, but no registered adapter can record its ordinary resolve/build", eco)
		}
		// Dependency is the same lane: what a lockfile scanner read, and none
		// ships for these (#387).
		candidate.Axis = serverstore.AuthoringAxisDependency
		if authoringCandidateEligible(candidate, request) {
			t.Errorf("%s Dependency was offered, but no registered adapter can read its lockfile", eco)
		}
		// These ecosystems still have sample verifier images. A missing ordinary
		// observer is not a claim that no sample can be authored or verified.
		candidate.Axis = serverstore.AuthoringAxisSample
		if !authoringCandidateEligible(candidate, request) {
			t.Errorf("%s Sample was incorrectly erased", eco)
		}
	}
	for _, eco := range []string{"npm", "pypi", "golang", "cargo"} {
		candidate := serverstore.WantedRow{Ecosystem: eco, Name: "ordinary-library", Version: "1.0.0", Kind: "EXPANSION", Axis: serverstore.AuthoringAxisEvidence}
		if !authoringCandidateEligible(candidate, request) {
			t.Errorf("%s observer-backed Evidence was removed", eco)
		}
	}
}
