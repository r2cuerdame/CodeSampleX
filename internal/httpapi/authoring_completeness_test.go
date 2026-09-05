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

func TestEvidenceOnlyGapCompletesInsideCachedAuthoringSnapshot(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "evidence-only-writer", testNow)
	const purl = "pkg:maven/org.jetbrains.kotlin.plugin.serialization.gradle.plugin@2.2.20"
	if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{
		PURL: purl, Ecosystem: "maven", Name: "org.jetbrains.kotlin.plugin.serialization.gradle.plugin",
		Version: "2.2.20", Publicness: "PUBLIC", LastSeen: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	first := requestAuthoringWork(t, srv.URL, token)
	work, _ := first["work"].(map[string]any)
	if first["status"] != "ASSIGNED" || work["axis"] != serverstore.AuthoringAxisEvidence || work["package"] != purl {
		t.Fatalf("first poll = %#v, want Evidence-only assignment", first)
	}

	accepted, rejected, err := store.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: testNow.Format("2006-01-02"), AnonID: "evidence-writer",
		ProjectBucket: "evidence-project", Package: purl, Direct: true,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "maven", OS: "linux", Arch: "amd64",
			Runtime: "java", RuntimeVersion: "21",
		},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
	}})
	if err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("evidence ingest: accepted=%d rejected=%v err=%v", accepted, rejected, err)
	}

	// The cached snapshot still contains the old Evidence and Sample rows.
	// The live predicate removes the completed Evidence row and the Sample N/A
	// rule removes the Gradle marker, so NO_WORK is now a justified answer.
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
	status, body := reportOutcome(t, srv.URL, token, `{"schemaVersion":1,"outcome":"NO_CALLABLE_SYMBOL","detail":"not an Evidence conclusion"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%v, want 400", status, body)
	}
	if _, held, err := store.AuthoringWorkForSubmission(t.Context(), "evidence-outcome-writer", "", testNow); err != nil || !held {
		t.Fatalf("rejected outcome released the claim: held=%v err=%v", held, err)
	}
}
