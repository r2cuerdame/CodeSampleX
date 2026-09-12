package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Issue #379: opt-in SAMPLE reservation for new claims.
//
// A worker that only wants Sample work must be able to say so. Without this,
// a SAMPLE-only farm receives Evidence or Dependency work it cannot produce,
// sits on the lease for 24 hours, and the coordinate is off the board for
// everybody. The reservation is additive: an empty value preserves the
// default mixed queue, and old servers reject the unknown field via
// DisallowUnknownFields — fail-closed by construction.

// Old servers reject unknown request fields with 400 via DisallowUnknownFields.
// This test asserts that property directly against the live handler.
func TestReservationFieldRejectedByDisallowUnknownFields(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "compat-writer", testNow)

	// A bogus field that the current struct does not know about must be
	// rejected with 400, proving the fail-closed contract any older server
	// enforces for new fields like "reservation".
	body := `{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22","this_field_does_not_exist":"anything"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
		bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field: status=%d, want 400", resp.StatusCode)
	}
}

// The server must reject unsupported reservation values.
func TestUnsupportedReservationValueRejected(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "bad-reservation-writer", testNow)

	for _, bad := range []string{"EVIDENCE", "DEPENDENCY", "ALL", "bogus"} {
		body, _ := json.Marshal(map[string]any{
			"schemaVersion":     1,
			"sandboxCapability": "CONTAINER_RUN",
			"verifierOS":        []string{"linux"},
			"clientVersion":     "v0.1.22",
			"reservation":       bad,
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
			bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("reservation=%q: status=%d, want 400", bad, resp.StatusCode)
		}
	}
}

// The SAMPLE reservation value must be accepted.
func TestSampleReservationValueAccepted(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "sample-reservation-writer", testNow)

	body, _ := json.Marshal(map[string]any{
		"schemaVersion":     1,
		"sandboxCapability": "CONTAINER_RUN",
		"verifierOS":        []string{"linux"},
		"clientVersion":     "v0.1.22",
		"reservation":       "SAMPLE",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// 200 proves the reservation field was accepted (NO_WORK is 200).
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reservation=SAMPLE: status=%d, want 200", resp.StatusCode)
	}
}

// When there are only EVIDENCE or DEPENDENCY candidates and the worker sends
// reservation=SAMPLE, the truthful answer is NO_WORK.
func TestSampleReservationReturnsNoWorkWhenOnlyNonSampleCandidatesExist(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "sample-only-writer", testNow)

	// Seed a package that only has EVIDENCE and DEPENDENCY gaps (no Sample
	// gap because @esbuild/win32-x64 is a platform-locked native addon).
	const purl = "pkg:npm/%40esbuild/win32-x64@0.25.0"
	if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "@esbuild/win32-x64",
		Version: "0.25.0", Publicness: "PUBLIC", LastSeen: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	// With reservation=SAMPLE and no SAMPLE candidates, NO_WORK is returned.
	body, _ := json.Marshal(map[string]any{
		"schemaVersion":     1,
		"sandboxCapability": "CONTAINER_RUN",
		"verifierOS":        []string{"linux"},
		"clientVersion":     "v0.1.22",
		"reservation":       "SAMPLE",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
		bytes.NewReader(body))
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
	if result["status"] != "NO_WORK" {
		t.Fatalf("expected NO_WORK when no SAMPLE candidates exist, got %v", result["status"])
	}
}

// Without a reservation, the default mixed queue is preserved — a poll can
// receive Evidence, Dependency, or Sample work.
func TestDefaultMixedBehaviourPreserved(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "mixed-writer", testNow)

	// Seed a package with no prior evidence — the completeness scheduler
	// will offer Evidence, Sample, and Dependency axes.
	const purl = "pkg:npm/mixedpkg@1.0.0"
	if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "mixedpkg",
		Version: "1.0.0", Publicness: "PUBLIC", LastSeen: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	// No reservation — default mixed behaviour.
	result := requestAuthoringWork(t, srv.URL, token)
	if result["status"] != "ASSIGNED" {
		t.Fatalf("mixed poll = %v, want ASSIGNED", result)
	}
}

// With reservation=SAMPLE, a poll that finds only SAMPLE candidates returns
// an assignment with axis=SAMPLE.
func TestSampleReservationServesOnlySampleAxis(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "sample-axis-writer", testNow)

	// Seed a package with no evidence — all three axes become candidates.
	const purl = "pkg:npm/sampleaxis@1.0.0"
	if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "sampleaxis",
		Version: "1.0.0", Publicness: "PUBLIC", LastSeen: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"schemaVersion":     1,
		"sandboxCapability": "CONTAINER_RUN",
		"verifierOS":        []string{"linux"},
		"clientVersion":     "v0.1.22",
		"reservation":       "SAMPLE",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Status string `json:"status"`
		Work   struct {
			Axis string `json:"axis"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ASSIGNED" {
		t.Fatalf("sample-reservation poll = %q, want ASSIGNED", result.Status)
	}
	if result.Work.Axis != serverstore.AuthoringAxisSample {
		t.Fatalf("served axis = %q, want %q", result.Work.Axis, serverstore.AuthoringAxisSample)
	}
}

// An existing claim on a non-SAMPLE axis is preserved when the worker polls
// with reservation=SAMPLE. The re-return path in ClaimAuthoringWork checks
// what the session already holds independently of the candidate list.
func TestExistingNonSampleClaimPreservedWithSampleReservation(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "existing-claim-writer", testNow)

	const purl = "pkg:npm/retained@1.0.0"
	if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "retained",
		Version: "1.0.0", Publicness: "PUBLIC", LastSeen: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	// Give the writer an Evidence claim directly through the store.
	evidenceWork := serverstore.WantedRow{
		Ecosystem: "npm", Name: "retained", Version: "1.0.0",
		Kind: "EXPANSION", Axis: serverstore.AuthoringAxisEvidence,
	}
	claimed, found, err := store.ClaimAuthoringWork(t.Context(), "existing-claim-writer",
		[]serverstore.WantedRow{evidenceWork}, testNow, testNow.Add(24*time.Hour))
	if err != nil || !found {
		t.Fatalf("direct evidence claim: found=%v err=%v", found, err)
	}
	if claimed.Axis != serverstore.AuthoringAxisEvidence {
		t.Fatalf("claimed axis = %q, want EVIDENCE", claimed.Axis)
	}
	// Now poll with reservation=SAMPLE. The existing EVIDENCE claim must be
	// returned because existing claims are preserved regardless of reservation.
	body, _ := json.Marshal(map[string]any{
		"schemaVersion":     1,
		"sandboxCapability": "CONTAINER_RUN",
		"verifierOS":        []string{"linux"},
		"clientVersion":     "v0.1.22",
		"reservation":       "SAMPLE",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Status string `json:"status"`
		Work   struct {
			Axis    string `json:"axis"`
			Package string `json:"package"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ASSIGNED" {
		t.Fatalf("existing claim poll = %q, want ASSIGNED", result.Status)
	}
	if result.Work.Axis != serverstore.AuthoringAxisEvidence {
		t.Fatalf("existing claim axis = %q, want EVIDENCE (preserved)", result.Work.Axis)
	}
}
