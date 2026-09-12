package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

type mockCandidateStore struct {
	*serverstore.Fake
	expansionRows []serverstore.WantedRow
	topWantedRows []serverstore.WantedRow
}

func (m *mockCandidateStore) ListAuthoringExpansionCandidates(ctx context.Context, limit int) ([]serverstore.WantedRow, error) {
	if len(m.expansionRows) > 0 {
		return m.expansionRows, nil
	}
	return m.Fake.ListAuthoringExpansionCandidates(ctx, limit)
}

func (m *mockCandidateStore) TopWanted(ctx context.Context, limit int) ([]serverstore.WantedRow, error) {
	if len(m.topWantedRows) > 0 {
		return m.topWantedRows, nil
	}
	return m.Fake.TopWanted(ctx, limit)
}

// Regression for blocker 1: When >400 non-SAMPLE candidates (e.g. EVIDENCE)
// rank ahead of eligible SAMPLE candidates, reservation=SAMPLE must filter SAMPLE
// candidates before buildAuthoringCandidates applies its 400-row cutoff, otherwise
// the higher-ranked non-SAMPLE rows hide all SAMPLE work and cause false NO_WORK.
func TestSampleReservationWithMoreThan400NonSampleCandidatesAheadOfSampleWork(t *testing.T) {
	fake := serverstore.NewFake()
	mock := &mockCandidateStore{Fake: fake}

	// 450 non-SAMPLE rows (EVIDENCE) with high score
	var expansion []serverstore.WantedRow
	for i := 0; i < 450; i++ {
		expansion = append(expansion, serverstore.WantedRow{
			Ecosystem: "npm",
			Name:      fmt.Sprintf("ev-pkg-%04d", i),
			Version:   "1.0.0",
			Kind:      "EXPANSION",
			Axis:      serverstore.AuthoringAxisEvidence,
			Score:     100,
		})
	}
	// 1 eligible SAMPLE row with lower score
	expansion = append(expansion, serverstore.WantedRow{
		Ecosystem: "npm",
		Name:      "sample-eligible",
		Version:   "1.0.0",
		Kind:      "EXPANSION",
		Axis:      serverstore.AuthoringAxisSample,
		Score:     1,
	})
	mock.expansionRows = expansion

	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Store = mock
	})

	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, fake, token, "sample-starve-writer", testNow)

	body, _ := json.Marshal(map[string]any{
		"schemaVersion":     1,
		"sandboxCapability": "CONTAINER_RUN",
		"verifierOS":        []string{"linux"},
		"clientVersion":     "v0.1.22",
		"reservation":       "SAMPLE",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result struct {
		Status string `json:"status"`
		Work   struct {
			Name string `json:"name"`
			Axis string `json:"axis"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ASSIGNED" {
		t.Fatalf("expected ASSIGNED for SAMPLE reservation with >400 non-SAMPLE rows, got status=%q", result.Status)
	}
	if result.Work.Axis != serverstore.AuthoringAxisSample || result.Work.Name != "sample-eligible" {
		t.Fatalf("expected sample-eligible SAMPLE work, got axis=%q name=%q", result.Work.Axis, result.Work.Name)
	}
}

// Regression for blocker 2: A reconstructed held claim must pass the same
// authoringCandidateEligible/environment eligibility gates as normal work,
// including TargetOS reconstruction. A worker whose environment changed must
// not retain an unusable 24h claim.
func TestHeldClaimReleasedWhenWorkerEnvironmentChanges(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "env-change-writer", testNow)

	// 1. Worker claims npm work on linux.
	npmWork := serverstore.WantedRow{
		Ecosystem: "npm", Name: "linux-only-pkg", Version: "1.0.0",
		Kind: "EXPANSION", Axis: serverstore.AuthoringAxisSample,
	}
	claimed, found, err := store.ClaimAuthoringWork(t.Context(), "env-change-writer",
		[]serverstore.WantedRow{npmWork}, testNow, testNow.Add(24*time.Hour))
	if err != nil || !found || claimed.Name != "linux-only-pkg" {
		t.Fatalf("initial claim failed: found=%v err=%v", found, err)
	}

	// 2. Worker reconnects declaring VerifierOS: ["windows"].
	// npm cannot run on windows (authoringRunnableOn("npm", "windows") is false).
	// The held claim is unusable by this worker, so it must NOT be retained.
	body, _ := json.Marshal(map[string]any{
		"schemaVersion":     1,
		"sandboxCapability": "CONTAINER_RUN",
		"verifierOS":        []string{"windows"},
		"clientVersion":     "v0.1.22",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result struct {
		Status string `json:"status"`
		Work   struct {
			Name string `json:"name"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	// It should NOT be assigned the unusable linux-only npm claim.
	if result.Status == "ASSIGNED" && result.Work.Name == "linux-only-pkg" {
		t.Fatal("worker retained unusable 24h claim after environment changed to windows")
	}

	// Verify the held claim was released in the store.
	if _, held, err := store.AuthoringWorkForSubmission(t.Context(), "env-change-writer", "", testNow); err != nil || held {
		t.Fatalf("unusable claim should be released in store: held=%v err=%v", held, err)
	}
}

func TestHeldClaimReleasedWhenTargetOSMismatches(t *testing.T) {
	fake := serverstore.NewFake()
	mock := &mockCandidateStore{Fake: fake}

	// A WANTED coordinate pinned to windows
	mock.topWantedRows = []serverstore.WantedRow{
		{
			Ecosystem: "golang", Name: "win-pipe", Version: "1.0.0", Symbol: "Open",
			Kind: "WANTED", Axis: serverstore.AuthoringAxisSample, TargetOS: "windows",
		},
	}

	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Store = mock
	})

	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, fake, token, "target-os-writer", testNow)

	// Worker initially claims the windows-pinned work on windows
	claimed, found, err := mock.ClaimAuthoringWork(t.Context(), "target-os-writer",
		mock.topWantedRows, testNow, testNow.Add(24*time.Hour))
	if err != nil || !found {
		t.Fatalf("initial claim failed: %v", err)
	}
	_ = claimed

	// Now worker reconnects declaring verifierOS: ["linux"].
	// The reconstructed held claim must reconstruct TargetOS="windows", which
	// fails authoringCandidateEligible on linux.
	body, _ := json.Marshal(map[string]any{
		"schemaVersion":     1,
		"sandboxCapability": "CONTAINER_RUN",
		"verifierOS":        []string{"linux"},
		"clientVersion":     "v0.1.22",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result struct {
		Status string `json:"status"`
		Work   struct {
			Name string `json:"name"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status == "ASSIGNED" && result.Work.Name == "win-pipe" {
		t.Fatal("worker retained windows-pinned claim when running on linux")
	}

	// Verify the held claim was released in the store.
	if _, held, err := mock.AuthoringWorkForSubmission(t.Context(), "target-os-writer", "", testNow); err != nil || held {
		t.Fatalf("mismatched targetOS claim should be released: held=%v err=%v", held, err)
	}
}

// Regression for blocker 3: A valid existing held claim must survive the later
// ranked sort/truncation even if it would rank below the 400-row cutoff.
func TestValidHeldClaimSurvivesRankedSortTruncation(t *testing.T) {
	for _, reservation := range []string{"", "SAMPLE"} {
		t.Run("reservation="+reservation, func(t *testing.T) {
			fake := serverstore.NewFake()
			mock := &mockCandidateStore{Fake: fake}

			// 450 higher-ranked candidates
			var expansion []serverstore.WantedRow
			for i := 0; i < 450; i++ {
				expansion = append(expansion, serverstore.WantedRow{
					Ecosystem: "npm",
					Name:      fmt.Sprintf("high-rank-%04d", i),
					Version:   "1.0.0",
					Kind:      "EXPANSION",
					Axis:      serverstore.AuthoringAxisSample,
					Score:     100,
				})
			}
			// The held candidate has Score: 0 (ranks below 400)
			heldCandidate := serverstore.WantedRow{
				Ecosystem: "npm",
				Name:      "low-score-held",
				Version:   "1.0.0",
				Kind:      "EXPANSION",
				Axis:      serverstore.AuthoringAxisSample,
				Score:     0,
			}
			expansion = append(expansion, heldCandidate)
			mock.expansionRows = expansion

			srv, _, _ := newTestServer(t, func(d *Deps) {
				d.Store = mock
			})

			const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
			authoringSession(t, fake, token, "held-cutoff-writer", testNow)

			// Give worker the low-score claim directly
			claimed, found, err := mock.ClaimAuthoringWork(t.Context(), "held-cutoff-writer",
				[]serverstore.WantedRow{heldCandidate}, testNow, testNow.Add(24*time.Hour))
			if err != nil || !found {
				t.Fatalf("failed to establish claim: %v", err)
			}
			_ = claimed

			reqBody := map[string]any{
				"schemaVersion":     1,
				"sandboxCapability": "CONTAINER_RUN",
				"verifierOS":        []string{"linux"},
				"clientVersion":     "v0.1.22",
			}
			if reservation != "" {
				reqBody["reservation"] = reservation
			}
			body, _ := json.Marshal(reqBody)
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			var result struct {
				Status string `json:"status"`
				Work   struct {
					Name string `json:"name"`
				} `json:"work"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if result.Status != "ASSIGNED" || result.Work.Name != "low-score-held" {
				t.Fatalf("held claim did not survive 400 cutoff: status=%q work=%+v", result.Status, result.Work)
			}
		})
	}
}

// An existing claim that was completed is NOT preserved.
func TestCompletedHeldClaimIsReleasedAcrossPoll(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "completed-held-writer", testNow)

	const purl = "pkg:npm/completed-target@1.0.0"
	if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "completed-target",
		Version: "1.0.0", Publicness: "PUBLIC", LastSeen: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	heldRow := serverstore.WantedRow{
		Ecosystem: "npm", Name: "completed-target", Version: "1.0.0",
		Kind: "EXPANSION", Axis: serverstore.AuthoringAxisSample,
	}
	if _, found, err := store.ClaimAuthoringWork(t.Context(), "completed-held-writer",
		[]serverstore.WantedRow{heldRow}, testNow, testNow.Add(24*time.Hour)); err != nil || !found {
		t.Fatalf("claim failed: %v", err)
	}

	// Now complete the sample by inserting a verified sample and PASS receipt.
	sampleID := "sample-completed-123"
	if err := store.SaveAuthoringDraft(t.Context(), serverstore.AuthoringDraftRow{
		SampleID: sampleID, SessionID: "completed-held-writer", CreatedAt: testNow, UpdatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{"packages":["` + purl + `"],"symbols":[""]}`
	if err := store.SaveSample(t.Context(), serverstore.SampleRow{
		SampleID: sampleID, ManifestJSON: manifestJSON, Status: "CROSS_PASS",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(t.Context(), serverstore.ReceiptRow{
		SampleID: sampleID, ContractResult: "PASS", CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	// Poll: FilterIncompleteAuthoringCandidates will drop completed-target because it has CROSS_PASS and PASS receipt.
	// Therefore the held claim must be released, not preserved.
	body, _ := json.Marshal(map[string]any{
		"schemaVersion":     1,
		"sandboxCapability": "CONTAINER_RUN",
		"verifierOS":        []string{"linux"},
		"clientVersion":     "v0.1.22",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var result struct {
		Status string `json:"status"`
		Work   struct {
			Name string `json:"name"`
			Axis string `json:"axis"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status == "ASSIGNED" && result.Work.Name == "completed-target" && result.Work.Axis == serverstore.AuthoringAxisSample {
		t.Fatal("completed held claim was preserved across poll")
	}

	if heldWork, held, err := store.AuthoringWorkForSubmission(t.Context(), "completed-held-writer", "", testNow); err == nil && held {
		if heldWork.Axis == serverstore.AuthoringAxisSample {
			t.Fatalf("completed sample claim should be released in store: %+v", heldWork)
		}
	}
}
