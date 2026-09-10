package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Issue #217 Acceptance Criteria:
// 1. Request-priority ordering (wanted & findings on requested packages served before generic expansion)
// 2. Generic fallback (generic expansion claimed when request queue is exhausted or leased)
// 3. Capability-based generic fallback (worker with linux falls back to generic linux work when request work is windows-pinned)
// 4. Deduplication (same coordinate in wanted and expansion deduped into single item preserving demand)
// 5. Boundedness (queue capped at max bound)
// 6. No starvation (package depth interleaves candidates across packages)

func TestBuildAuthoringCandidates_TiersAndOrdering(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	requested := []serverstore.WantedRow{
		{Ecosystem: "npm", Name: "req-pkg-a", Version: "1.0.0", Asks: 3},
		{Ecosystem: "npm", Name: "req-pkg-b", Version: "1.0.0", Asks: 1},
	}

	candidates := []serverstore.WantedRow{
		// Tier 5: generic empty coordinate (score = 0)
		{Ecosystem: "npm", Name: "unreq-pkg-e", Version: "1.0.0", Kind: "EXPANSION", Score: 0, Axis: serverstore.AuthoringAxisSample},
		// Tier 4: other observed demand (score > 0 on unrequested pkg)
		{Ecosystem: "npm", Name: "unreq-pkg-d", Version: "1.0.0", Kind: "EXPANSION", Score: 10, Axis: serverstore.AuthoringAxisSample},
		// Tier 3: completeness coverage hole on requested package (Evidence axis)
		{Ecosystem: "npm", Name: "req-pkg-a", Version: "1.0.0", Kind: "EXPANSION", Score: 4, Axis: serverstore.AuthoringAxisEvidence},
		// Tier 2: other failure cluster (FINDING on unrequested pkg)
		{Ecosystem: "npm", Name: "unreq-pkg-c", Version: "1.0.0", Kind: "FINDING", Score: 8, Axis: serverstore.AuthoringAxisSample},
		// Tier 1: direct single-ask WANTED
		{Ecosystem: "npm", Name: "req-pkg-b", Version: "1.0.0", Kind: "WANTED", Asks: 1, Score: 1, Axis: serverstore.AuthoringAxisSample},
		// Tier 1: boundary coverage (Sample axis) on requested package
		{Ecosystem: "npm", Name: "req-pkg-a", Version: "1.1.0", Kind: "EXPANSION", Score: 2, Axis: serverstore.AuthoringAxisSample},
		// Tier 0: direct repeat-ask WANTED (Asks > 1)
		{Ecosystem: "npm", Name: "req-pkg-a", Version: "1.0.0", Kind: "WANTED", Asks: 3, Score: 3, Axis: serverstore.AuthoringAxisSample},
		// Tier 0: failure cluster (FINDING) on requested package
		{Ecosystem: "npm", Name: "req-pkg-b", Version: "2.0.0", Kind: "FINDING", Score: 5, Axis: serverstore.AuthoringAxisSample, LastSeen: now},
	}

	req := authoringWorkRequest{
		SchemaVersion:     1,
		SandboxCapability: domain.CapContainerRun,
		VerifierOS:        []string{"linux"},
		ClientVersion:     "v0.1.22",
	}

	out := buildAuthoringCandidates(candidates, requested, req)
	if len(out) != len(candidates) {
		t.Fatalf("got %d candidates, want %d", len(out), len(candidates))
	}

	// Verify tiers descend monotonically
	for i := 0; i < len(out)-1; i++ {
		tA := candidateTier(out[i], map[[2]string]bool{{"npm", "req-pkg-a"}: true, {"npm", "req-pkg-b"}: true})
		tB := candidateTier(out[i+1], map[[2]string]bool{{"npm", "req-pkg-a"}: true, {"npm", "req-pkg-b"}: true})
		if tA > tB {
			t.Fatalf("tier inversion at %d -> %d: %s@%s (tier %d) placed before %s@%s (tier %d)",
				i, i+1, out[i].Name, out[i].Version, tA, out[i+1].Name, out[i+1].Version, tB)
		}
	}

	// Verify top two are Tier 0
	reqPkgs := map[[2]string]bool{{"npm", "req-pkg-a"}: true, {"npm", "req-pkg-b"}: true}
	if t0 := candidateTier(out[0], reqPkgs); t0 != tier0RepeatedAndFindings {
		t.Errorf("out[0] tier = %d, want tier 0", t0)
	}
	if t1 := candidateTier(out[1], reqPkgs); t1 != tier0RepeatedAndFindings {
		t.Errorf("out[1] tier = %d, want tier 0", t1)
	}

	// Verify last is Tier 5 (generic empty coordinate)
	if tLast := candidateTier(out[len(out)-1], reqPkgs); tLast != tier5GenericExpansion {
		t.Errorf("out[last] tier = %d, want tier 5", tLast)
	}
	if out[len(out)-1].Name != "unreq-pkg-e" {
		t.Errorf("out[last] name = %q, want unreq-pkg-e", out[len(out)-1].Name)
	}
}

func TestBuildAuthoringCandidates_Deduplication(t *testing.T) {
	t1 := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

	candidates := []serverstore.WantedRow{
		{
			Ecosystem: "npm", Name: "shared-pkg", Version: "1.0.0", Symbol: "run",
			TargetOS: "linux", Axis: "SAMPLE", Kind: "WANTED", Asks: 2, Score: 2,
			LastSeen: t1,
		},
		{
			Ecosystem: "npm", Name: "shared-pkg", Version: "1.0.0", Symbol: "run",
			TargetOS: "linux", Axis: "SAMPLE", Kind: "FINDING", Asks: 0, Score: 15,
			LastSeen: t2,
		},
		{
			Ecosystem: "npm", Name: "shared-pkg", Version: "1.0.0", Symbol: "run",
			TargetOS: "linux", Axis: "SAMPLE", Kind: "EXPANSION", Asks: 0, Score: 1,
			LastSeen: t1,
		},
	}

	req := authoringWorkRequest{
		SchemaVersion:     1,
		SandboxCapability: domain.CapContainerRun,
		VerifierOS:        []string{"linux"},
		ClientVersion:     "v0.1.22",
	}

	out := buildAuthoringCandidates(candidates, nil, req)
	if len(out) != 1 {
		t.Fatalf("expected 1 deduplicated candidate, got %d", len(out))
	}

	merged := out[0]
	if merged.Name != "shared-pkg" || merged.Version != "1.0.0" || merged.Symbol != "run" {
		t.Errorf("coordinate mismatch: %v", merged)
	}
	if merged.Asks != 2 {
		t.Errorf("asks = %d, want 2 (highest demand preserved)", merged.Asks)
	}
	if merged.Score != 15 {
		t.Errorf("score = %d, want 15 (highest score preserved)", merged.Score)
	}
	if merged.Kind != "WANTED" {
		t.Errorf("kind = %q, want WANTED (promoted from asks)", merged.Kind)
	}
	if !merged.LastSeen.Equal(t2) {
		t.Errorf("lastSeen = %v, want %v (most recent preserved)", merged.LastSeen, t2)
	}
}

func TestBuildAuthoringCandidates_NoStarvation_PkgDepth(t *testing.T) {
	// Package "prolific" has 5 versions, "sparse" has 1 version. Both are Tier 1.
	var candidates []serverstore.WantedRow
	for v := 1; v <= 5; v++ {
		candidates = append(candidates, serverstore.WantedRow{
			Ecosystem: "npm", Name: "prolific", Version: fmt.Sprintf("%d.0.0", v),
			Kind: "WANTED", Asks: 1, Score: 1,
		})
	}
	candidates = append(candidates, serverstore.WantedRow{
		Ecosystem: "npm", Name: "sparse", Version: "1.0.0",
		Kind: "WANTED", Asks: 1, Score: 1,
	})

	req := authoringWorkRequest{
		SchemaVersion:     1,
		SandboxCapability: domain.CapContainerRun,
		VerifierOS:        []string{"linux"},
		ClientVersion:     "v0.1.22",
	}

	out := buildAuthoringCandidates(candidates, nil, req)
	if len(out) != 6 {
		t.Fatalf("expected 6 candidates, got %d", len(out))
	}

	// Depth 1 must contain one candidate from "prolific" and one from "sparse".
	// Therefore position 0 and 1 must be from the two different packages.
	namesAtDepth1 := map[string]bool{out[0].Name: true, out[1].Name: true}
	if !namesAtDepth1["prolific"] || !namesAtDepth1["sparse"] {
		t.Fatalf("pkgDepth starvation! Position 0 and 1: %s, %s; want one prolific and one sparse",
			out[0].Name, out[1].Name)
	}

	// For prolific, newest version (5.0.0) must be chosen first
	for _, c := range out[:2] {
		if c.Name == "prolific" && c.Version != "5.0.0" {
			t.Errorf("prolific at depth 1 has version %s, want newest 5.0.0", c.Version)
		}
	}
}

func TestBuildAuthoringCandidates_Boundedness(t *testing.T) {
	var candidates []serverstore.WantedRow
	for i := 0; i < 450; i++ {
		candidates = append(candidates, serverstore.WantedRow{
			Ecosystem: "npm", Name: fmt.Sprintf("pkg-%04d", i), Version: "1.0.0",
			Kind: "EXPANSION", Score: int64(i),
		})
	}

	req := authoringWorkRequest{
		SchemaVersion:     1,
		SandboxCapability: domain.CapContainerRun,
		VerifierOS:        []string{"linux"},
		ClientVersion:     "v0.1.22",
	}

	out := buildAuthoringCandidates(candidates, nil, req)
	if len(out) != maxOfferedCandidates {
		t.Fatalf("got %d candidates, want bounded %d", len(out), maxOfferedCandidates)
	}
}

func TestAuthoringWork_GenericFallbackWhenRequestExhausted(t *testing.T) {
	// Store with 2 WANTED items and 2 EXPANSION items.
	// WANTED must be served on polls 1 and 2.
	// Once exhausted, generic fallback must serve EXPANSION on polls 3 and 4.
	store := newSnapshotStore(
		serverstore.WantedRow{Ecosystem: "npm", Name: "exp-1", Version: "1.0.0", Kind: "EXPANSION"},
		serverstore.WantedRow{Ecosystem: "npm", Name: "exp-2", Version: "1.0.0", Kind: "EXPANSION"},
	)
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Store = store })
	s := srv.URL

	// Seed 2 WANTED items
	rows := []serverstore.WantedRow{
		{Ecosystem: "npm", Name: "w-1", Version: "1.0.0", Symbol: "run"},
		{Ecosystem: "npm", Name: "w-2", Version: "1.0.0", Symbol: "run"},
	}
	if err := store.RecordWanted(t.Context(), testNow.Format("2006-01-02"), "0123456789abcdef", rows); err != nil {
		t.Fatal(err)
	}

	var results []string
	for n := 1; n <= 5; n++ {
		results = append(results, pollOnce(t, s, store, n))
	}

	expected := []string{"WANTED", "WANTED", "EXPANSION", "EXPANSION", ""}
	for i, exp := range expected {
		if results[i] != exp {
			t.Errorf("poll %d returned %q, want %q", i+1, results[i], exp)
		}
	}
}

func TestAuthoringWork_CapabilityBasedFallback(t *testing.T) {
	// Store with 1 WANTED item pinned to "windows", and 1 generic EXPANSION item runnable on linux.
	// A Linux worker must not get stuck or receive NO_WORK; it must fall back to the generic Linux work.
	store := newSnapshotStore(
		serverstore.WantedRow{Ecosystem: "npm", Name: "linux-gap", Version: "1.0.0", Kind: "EXPANSION", TargetOS: "linux"},
	)
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Store = store })
	s := srv.URL

	// Record WANTED with windows pinned
	rows := []serverstore.WantedRow{
		{Ecosystem: "npm", Name: "win-only-pkg", Version: "1.0.0", TargetOS: "windows"},
	}
	if err := store.RecordWanted(t.Context(), testNow.Format("2006-01-02"), "0123456789abcdef", rows); err != nil {
		t.Fatal(err)
	}

	// Poll from a Linux worker
	token := "csx_author_v1_" + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%032d", 999)))
	sum := sha256.Sum256([]byte(token))
	now := testNow
	if err := store.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: hex.EncodeToString(sum[:]), SessionID: "linux-worker", Label: "slot1",
		Model: "claude-haiku", Reasoning: "low", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPost, s+"/v1/authoring/work/next",
		bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Work   struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	if body.Status != "ASSIGNED" {
		t.Fatalf("status = %q, want ASSIGNED", body.Status)
	}
	if body.Work.Name != "linux-gap" {
		t.Errorf("claimed %q, want linux-gap (fallback from windows-pinned request)", body.Work.Name)
	}
}
