package web

import (
	"strings"
	"testing"
)

// Issue #178: Test 1 - Dependency version changed across releases, but both releases passed.
// Must NOT be marked as a problem; should be marked as changed without linked failure.
func TestDependencyChangedAcrossReleasesWithoutFailure(t *testing.T) {
	matrix := buildDependencyMatrix("npm", []DependencyEdge{
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "helper", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "helper", ChildVersion: "2.0.0"},
	})
	deps := []PackageDep{
		{Library: "helper", Version: "2.0.0", State: "verified"},
	}

	evaluated, summary := evaluateDependencyHealth("npm", "app", "2.0.0", deps, nil, matrix, "en")
	if summary.ProblemsCount != 0 {
		t.Errorf("ProblemsCount = %d, want 0", summary.ProblemsCount)
	}
	if summary.ChangedCount != 1 {
		t.Errorf("ChangedCount = %d, want 1", summary.ChangedCount)
	}
	if summary.HasBreak {
		t.Error("HasBreak is true, want false")
	}
	if len(evaluated) != 1 || evaluated[0].Health != "changed" {
		t.Errorf("evaluated[0].Health = %q, want changed", evaluated[0].Health)
	}
	if evaluated[0].HealthBadge != "CHANGED" {
		t.Errorf("HealthBadge = %q, want CHANGED", evaluated[0].HealthBadge)
	}
}

// Issue #178: Test 2 - Parent release has failure evidence, but dependency version did not change.
func TestParentFailedWithoutDependencyChange(t *testing.T) {
	matrix := buildDependencyMatrix("npm", []DependencyEdge{
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "helper", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "helper", ChildVersion: "1.0.0"},
	})
	deps := []PackageDep{
		{Library: "helper", Version: "1.0.0", State: "verified"},
	}
	clusters := []failureCluster{
		{
			Versions:    []string{"2.0.0"},
			Stage:       "test",
			Fingerprint: "ERR_ASSERT",
			Count:       5,
		},
	}

	evaluated, summary := evaluateDependencyHealth("npm", "app", "2.0.0", deps, clusters, matrix, "en")
	if summary.ProblemsCount != 1 {
		t.Errorf("ProblemsCount = %d, want 1", summary.ProblemsCount)
	}
	if !summary.HasBreak {
		t.Error("HasBreak is false, want true")
	}
	if summary.FirstBreak.ChildLibrary != "" {
		t.Errorf("FirstBreak.ChildLibrary = %q, want empty (unmoved)", summary.FirstBreak.ChildLibrary)
	}
	if evaluated[0].IsMover {
		t.Error("IsMover is true, want false")
	}
}

// Issue #178: Test 3 - Child standalone contract is PASS, but combination under this release has linked failure.
// Edge health must be FAIL, not green.
func TestChildStandalonePassWithCombinationFailure(t *testing.T) {
	matrix := buildDependencyMatrix("npm", []DependencyEdge{
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "dep", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "dep", ChildVersion: "2.0.0"},
	})
	deps := []PackageDep{
		{Library: "dep", Version: "2.0.0", State: "verified"}, // Child itself is verified/pass
	}
	clusters := []failureCluster{
		{
			Versions:    []string{"2.0.0"},
			Stage:       "compile",
			Fingerprint: "ERR_REQUIRE_ESM",
			Count:       8,
			EnvSummary:  map[string]string{"os": "windows", "runtime": "node@22"},
		},
	}

	evaluated, summary := evaluateDependencyHealth("npm", "app", "2.0.0", deps, clusters, matrix, "en")
	if len(evaluated) != 1 {
		t.Fatalf("len(evaluated) = %d, want 1", len(evaluated))
	}
	if evaluated[0].Health != "fail" {
		t.Errorf("Health = %q, want fail", evaluated[0].Health)
	}
	if evaluated[0].HealthTone != "red" {
		t.Errorf("HealthTone = %q, want red", evaluated[0].HealthTone)
	}
	if evaluated[0].State != "verified" {
		t.Errorf("Standalone State = %q, want verified preserved", evaluated[0].State)
	}
	if summary.FirstBreak == nil || summary.FirstBreak.ChildLibrary != "dep" {
		t.Errorf("FirstBreak.ChildLibrary = %+v, want dep", summary.FirstBreak)
	}
}

// Issue #178: Test 4 - Both PASS and FAIL observations coexist for the parent release -> MIXED.
func TestMixedEvidenceProducesMixedHealth(t *testing.T) {
	matrix := buildDependencyMatrix("npm", []DependencyEdge{
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "dep", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "dep", ChildVersion: "1.0.0"},
	})
	deps := []PackageDep{
		{Library: "dep", Version: "1.0.0", State: "observed"},
	}
	clusters := []failureCluster{
		{
			Versions:         []string{"2.0.0"},
			Stage:            "test",
			Count:            3,
			ObservationCount: 10, // 3 fails out of 10 observations -> 7 pass, 3 fail
		},
	}

	evaluated, summary := evaluateDependencyHealth("npm", "app", "2.0.0", deps, clusters, matrix, "en")
	if summary.MixedCount != 1 {
		t.Errorf("MixedCount = %d, want 1", summary.MixedCount)
	}
	if evaluated[0].Health != "mixed" {
		t.Errorf("Health = %q, want mixed", evaluated[0].Health)
	}
	if evaluated[0].HealthTone != "yellow" {
		t.Errorf("HealthTone = %q, want yellow", evaluated[0].HealthTone)
	}
}

// Issue #178: Test 5 - Complete tree without child vs unmeasured tree.
// Matrix cells must have distinct State: "not_in_tree" vs "unmeasured".
func TestBlankSemanticsDifferentiatesNotInTreeFromUnmeasured(t *testing.T) {
	matrix := buildDependencyMatrix("npm", []DependencyEdge{
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "childA", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "childA", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "childB", ChildVersion: "1.0.0"},
	})
	if matrix == nil {
		t.Fatal("matrix is nil")
	}

	var rowB *dependencyMatrixRow
	for i := range matrix.Rows {
		if matrix.Rows[i].Child == "childB" {
			rowB = &matrix.Rows[i]
		}
	}
	if rowB == nil {
		t.Fatal("childB row missing")
	}

	if rowB.Cells[0].State != "not_in_tree" {
		t.Errorf("cell[0].State = %q, want not_in_tree", rowB.Cells[0].State)
	}
	if !strings.Contains(rowB.Cells[0].Title, "Not declared") {
		t.Errorf("cell[0].Title = %q, want to contain 'Not declared'", rowB.Cells[0].Title)
	}

	if rowB.Cells[1].State != "version" || rowB.Cells[1].Version != "1.0.0" {
		t.Errorf("cell[1] = %+v, want version 1.0.0", rowB.Cells[1])
	}
}

// Issue #178: Test 6 - Rendered HTML integration test.
func TestDependencyHealthHTMLRendering(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.dependencies = []DependencyEdge{
		{ParentVersion: "2.0.0", ChildName: "alpha", ChildVersion: "2.0.0"},
		{ParentVersion: "1.0.0", ChildName: "alpha", ChildVersion: "1.0.0"},
		{ParentVersion: "2.0.0", ChildName: "beta", ChildVersion: "1.0.0"},
		{ParentVersion: "1.0.0", ChildName: "beta", ChildVersion: "1.0.0"},
	}
	store.clusters["npm|axios"] = []string{
		`{"stage":"test","fingerprint":"ERR_REQUIRE_ESM","count":5,"versions":["2.0.0"],"envSummary":{"os":"windows","runtime":"node@22"}}`,
	}

	body := mustGet(t, mux, "/npm/axios?f_version=2.0.0")

	// Section and Summary
	mustContain(t, body, `id="dependency-health"`)
	mustContain(t, body, `class="dephealth-summary`)
	mustContain(t, body, `class="dephealth-break"`)
	mustContain(t, body, "First observed break")
	mustContain(t, body, "5 FAIL")
	mustContain(t, body, "ERR_REQUIRE_ESM")
	mustContain(t, body, "windows · node@22")

	// Health badges in table
	mustContain(t, body, "badge-red")
	mustContain(t, body, "FAIL")

	// Matrix cell state
	mustContain(t, body, `cell-version`)
}
