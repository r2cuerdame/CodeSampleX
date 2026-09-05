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
		{Library: "dep", Version: "2.0.0", State: "verified", SameReceipt: true, Outcome: "fail"}, // Same receipt proven failure
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
		{Library: "dep", Version: "1.0.0", State: "observed", SameReceipt: true, Outcome: "mixed"},
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
		{ParentVersion: "2.0.0", ChildName: "alpha", ChildVersion: "2.0.0", SameReceipt: true, Outcome: "fail"},
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

// Issue #178: Test 7 - Unpinned page HTML rendering.
// When no version is pinned, Dependency Health summary and cross-release matrix
// must still be prominently displayed, without the single-release table.
func TestDependencyHealthUnpinnedHTMLRendering(t *testing.T) {
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

	body := mustGet(t, mux, "/npm/axios")

	// Section, Heading and Summary must be present even without ?f_version
	mustContain(t, body, `id="dependency-health"`)
	mustContain(t, body, `class="dephealth-summary`)
	mustContain(t, body, `class="dephealth-break"`)
	mustContain(t, body, "First observed break")
	mustContain(t, body, "5 FAIL")
	mustContain(t, body, "ERR_REQUIRE_ESM")

	// Matrix must be rendered directly in this section
	mustContain(t, body, `class="depmatrix"`)
	mustContain(t, body, `cell-version`)

	// Single release table must NOT be rendered on unpinned page
	if strings.Contains(body, `<table class="shipswith">`) {
		t.Error("the unpinned page printed single release table")
	}
}

// Issue #178: Regression test - Same-receipt vs cross-receipt evidence.
// Parent failure must not be attributed to dependencies unless same receipt proves exact combination.
func TestSameReceiptVsCrossReceiptEvidence(t *testing.T) {
	matrix := buildDependencyMatrix("npm", []DependencyEdge{
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "proven_fail", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "proven_fail", ChildVersion: "2.0.0"},
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "cross_mover", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "cross_mover", ChildVersion: "2.0.0"},
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "cross_steady_pass", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "cross_steady_pass", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "1.0.0", ChildName: "cross_steady_unknown", ChildVersion: "1.0.0"},
		{ParentName: "app", ParentVersion: "2.0.0", ChildName: "cross_steady_unknown", ChildVersion: "1.0.0"},
	})
	deps := []PackageDep{
		{Library: "proven_fail", Version: "2.0.0", State: "verified", SameReceipt: true, Outcome: "fail"},
		{Library: "cross_mover", Version: "2.0.0", State: "verified", SameReceipt: false},
		{Library: "cross_steady_pass", Version: "1.0.0", State: "verified", SameReceipt: false},
		{Library: "cross_steady_unknown", Version: "1.0.0", State: "none", SameReceipt: false},
	}
	clusters := []failureCluster{
		{
			Versions:         []string{"2.0.0"},
			Stage:            "test",
			Fingerprint:      "ERR_ASSERT",
			Count:            4,
			ObservationCount: 4,
			EnvSummary:       map[string]string{"os": "linux", "runtime": "node@22"},
		},
	}

	evaluated, summary := evaluateDependencyHealth("npm", "app", "2.0.0", deps, clusters, matrix, "en")

	byName := map[string]PackageDep{}
	for _, d := range evaluated {
		byName[d.Library] = d
	}

	// 1. Same-receipt failure on proven_fail must be FAIL / red.
	pf := byName["proven_fail"]
	if pf.Health != "fail" || pf.HealthBadge != "FAIL" || pf.HealthTone != "red" {
		t.Errorf("proven_fail = %+v, want Health=fail, Badge=FAIL, Tone=red", pf)
	}

	// 2. Cross-receipt mover must NOT be marked FAIL. Must have honest candidate semantics.
	cm := byName["cross_mover"]
	if cm.Health != "candidate" || cm.HealthBadge != "CANDIDATE" || cm.HealthTone != "yellow" {
		t.Errorf("cross_mover = %+v, want Health=candidate, Badge=CANDIDATE, Tone=yellow", cm)
	}

	// 3. Cross-receipt unmoved with verified state must NOT be marked FAIL or MIXED. Must be PASS / green.
	csp := byName["cross_steady_pass"]
	if csp.Health != "pass" || csp.HealthBadge != "PASS" || csp.HealthTone != "green" {
		t.Errorf("cross_steady_pass = %+v, want Health=pass, Badge=PASS, Tone=green", csp)
	}

	// 4. Cross-receipt unmoved unmeasured must be UNKNOWN / dim.
	csu := byName["cross_steady_unknown"]
	if csu.Health != "unknown" || csu.HealthBadge != "UNKNOWN" || csu.HealthTone != "dim" {
		t.Errorf("cross_steady_unknown = %+v, want Health=unknown, Badge=UNKNOWN, Tone=dim", csu)
	}

	// Summary checks:
	// Only proven_fail is an observed problem count from edges.
	if summary.ProblemsCount != 1 {
		t.Errorf("ProblemsCount = %d, want 1", summary.ProblemsCount)
	}
	if summary.FirstBreak == nil || summary.FirstBreak.ChildLibrary != "proven_fail" {
		t.Errorf("FirstBreak.ChildLibrary = %v, want proven_fail", summary.FirstBreak)
	}
}

// Issue #178: Regression test - Movement-only non-failure.
// Dependency changes without linked failure must not be exaggerated into problems.
func TestMovementOnlyNonFailure(t *testing.T) {
	matrix := buildDependencyMatrix("npm", []DependencyEdge{
		{ParentName: "ajv", ParentVersion: "7.0.0", ChildName: "fast-uri", ChildVersion: "3.1.0"},
		{ParentName: "ajv", ParentVersion: "8.0.0", ChildName: "fast-uri", ChildVersion: "3.1.4"},
		{ParentName: "ajv", ParentVersion: "7.0.0", ChildName: "json-schema-traverse", ChildVersion: "0.4.1"},
		{ParentName: "ajv", ParentVersion: "8.0.0", ChildName: "json-schema-traverse", ChildVersion: "1.0.0"},
		{ParentName: "ajv", ParentVersion: "7.0.0", ChildName: "require-from-string", ChildVersion: "2.0.2"},
		{ParentName: "ajv", ParentVersion: "8.0.0", ChildName: "require-from-string", ChildVersion: "2.0.2"},
		{ParentName: "ajv", ParentVersion: "7.0.0", ChildName: "uri-js", ChildVersion: "4.4.1"},
		{ParentName: "ajv", ParentVersion: "8.0.0", ChildName: "uri-js", ChildVersion: "4.4.1"},
	})
	deps := []PackageDep{
		{Library: "fast-uri", Version: "3.1.4", State: "verified"},
		{Library: "json-schema-traverse", Version: "1.0.0", State: "verified"},
		{Library: "require-from-string", Version: "2.0.2", State: "verified"},
		{Library: "uri-js", Version: "4.4.1", State: "verified"},
	}

	evaluated, summary := evaluateDependencyHealth("npm", "ajv", "8.0.0", deps, nil, matrix, "en")

	if summary.ProblemsCount != 0 {
		t.Errorf("ProblemsCount = %d, want 0", summary.ProblemsCount)
	}
	if summary.HasBreak {
		t.Error("HasBreak is true, want false")
	}
	if summary.ChangedCount != 2 {
		t.Errorf("ChangedCount = %d, want 2", summary.ChangedCount)
	}
	if summary.SteadyCount != 2 {
		t.Errorf("SteadyCount = %d, want 2", summary.SteadyCount)
	}

	for _, d := range evaluated {
		if d.Library == "fast-uri" || d.Library == "json-schema-traverse" {
			if d.Health != "changed" || d.HealthBadge != "CHANGED" || d.HealthTone != "blue" {
				t.Errorf("%s = %+v, want changed/CHANGED/blue", d.Library, d)
			}
		} else {
			if d.Health != "pass" || d.HealthBadge != "PASS" || d.HealthTone != "green" {
				t.Errorf("%s = %+v, want pass/PASS/green", d.Library, d)
			}
		}
	}
}

// Issue #178: Regression test - Deep link restoration.
// View exact conditions link must preserve environment coordinates and reload must restore them.
func TestDeepLinkRestoration(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.dependencies = []DependencyEdge{
		{ParentVersion: "27.3.0", ChildName: "dom-selector", ChildVersion: "6.7.6"},
		{ParentVersion: "26.0.0", ChildName: "dom-selector", ChildVersion: "5.0.0"},
	}
	store.clusters["npm|jsdom"] = []string{
		`{"stage":"PROJECT_COMPILE","fingerprint":"sha256:0470f88d95bb52932e5704cdc30cfcecbd43ae71af54d4c5f93d16aa27317cd2","count":4,"observationCount":4,"versions":["27.3.0"],"envSummary":{"os":"windows","runtime":"node@24.13","executionContext":"node","moduleSystem":"cjs"}}`,
	}

	// 1. Fetch package page with f_version=27.3.0
	body := mustGet(t, mux, "/npm/jsdom?f_version=27.3.0&lang=en")

	// Verify deep link href contains environment coordinates
	mustContain(t, body, `f_os=windows`)
	mustContain(t, body, `f_runtime=node%4024.13`)
	mustContain(t, body, `f_context=node`)
	mustContain(t, body, `f_moduleSystem=cjs`)
	mustContain(t, body, `f_version=27.3.0`)
	mustContain(t, body, `#cube`)

	// 2. Simulate clicking / reloading that exact conditions deep link
	deepLink := "/npm/jsdom?f_context=node&f_moduleSystem=cjs&f_os=windows&f_runtime=node%4024.13&f_version=27.3.0&lang=en#cube"
	reloaded := mustGet(t, mux, deepLink)

	// Ensure exact conditions restored:
	// Section and break detail present
	mustContain(t, reloaded, `id="dependency-health"`)
	mustContain(t, reloaded, `27.3.0`)
	mustContain(t, reloaded, `PROJECT_COMPILE`)
	mustContain(t, reloaded, `sha256:0470f88d95bb52932e5704cdc30cfcecbd43ae71af54d4c5f93d16aa27317cd2`)

	// Ensure no false arrow blaming dom-selector as proven break
	if strings.Contains(reloaded, `dom-selector@6.7.6</strong>`) {
		t.Errorf("reloaded page contains false break arrow blaming dom-selector")
	}
}
