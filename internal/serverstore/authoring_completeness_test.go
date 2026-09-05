package serverstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func seedCompletenessPackage(t *testing.T, store expansionStore, name string, now time.Time) string {
	t.Helper()
	purl := "pkg:npm/" + name + "@1.0.0"
	if err := store.UpsertPackage(context.Background(), PackageRow{
		PURL: purl, Ecosystem: "npm", Name: name, Version: "1.0.0",
		Major: "1", Publicness: "PUBLIC", LastSeen: now,
	}); err != nil {
		t.Fatal(err)
	}
	return purl
}

func seedCompletenessEvidence(t *testing.T, store expansionStore, purl, name string, now time.Time, dependencyNone bool) {
	t.Helper()
	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: now.Format("2006-01-02"),
		AnonID: "anon-" + name, ProjectBucket: "project-" + name,
		Package: purl, Direct: true, DependsOnNone: dependencyNone,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22",
		},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
	}
	accepted, rejected, err := store.IngestBatches(context.Background(), []domain.ObservationBatch{batch})
	if err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest %s: accepted=%d rejected=%v err=%v", purl, accepted, rejected, err)
	}
}

func axesInRows(rows []WantedRow, name string) map[string]bool {
	axes := make(map[string]bool)
	for _, row := range rows {
		if row.Name == name && row.Version == "1.0.0" {
			axes[normalizeAuthoringAxis(row.Axis)] = true
		}
	}
	return axes
}

func TestCompletenessSchedulerConvergesAfterEachAxisCompletes(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	purl := seedCompletenessPackage(t, store, "converge", now)

	snapshot, err := store.ListAuthoringExpansionCandidates(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if got := axesInRows(snapshot, "converge"); len(got) != 3 || !got[AuthoringAxisSample] || !got[AuthoringAxisEvidence] || !got[AuthoringAxisDependency] {
		t.Fatalf("unmeasured axes = %v, want all three missing deliverables", got)
	}

	seedCompletenessEvidence(t, store, purl, "converge", now, false)
	live, err := store.FilterIncompleteAuthoringCandidates(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := axesInRows(live, "converge"); len(got) != 2 || !got[AuthoringAxisSample] || !got[AuthoringAxisDependency] {
		t.Fatalf("observed axes = %v, want Sample and Dependency", got)
	}

	seedVerifiedSample(t, store, context.Background(), purl, "linux", now)
	live, err = store.FilterIncompleteAuthoringCandidates(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := axesInRows(live, "converge"); len(got) != 1 || !got[AuthoringAxisDependency] {
		t.Fatalf("sampled axes = %v, want Dependency only", got)
	}

	seedCompletenessEvidence(t, store, purl, "converge-none", now.Add(time.Hour), true)
	live, err = store.FilterIncompleteAuthoringCandidates(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := axesInRows(live, "converge"); len(got) != 0 {
		t.Fatalf("complete coordinate still has work: %v", got)
	}
}

func TestIntegrationCompletenessSchedulerConvergesPostgres(t *testing.T) {
	store := openTestPG(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	purl := seedCompletenessPackage(t, store, "pg-converge", now)
	snapshot, err := store.ListAuthoringExpansionCandidates(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if got := axesInRows(snapshot, "pg-converge"); len(got) != 3 || !got[AuthoringAxisSample] || !got[AuthoringAxisEvidence] || !got[AuthoringAxisDependency] {
		t.Fatalf("PostgreSQL unmeasured axes = %v, want all three", got)
	}

	seedCompletenessEvidence(t, store, purl, "pg-converge", now, false)
	live, err := store.FilterIncompleteAuthoringCandidates(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := axesInRows(live, "pg-converge"); len(got) != 2 || !got[AuthoringAxisSample] || !got[AuthoringAxisDependency] {
		t.Fatalf("PostgreSQL observed axes = %v, want Sample and Dependency", got)
	}

	seedVerified(t, store, purl, "linux", now)
	live, err = store.FilterIncompleteAuthoringCandidates(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := axesInRows(live, "pg-converge"); len(got) != 1 || !got[AuthoringAxisDependency] {
		t.Fatalf("PostgreSQL sampled axes = %v, want Dependency", got)
	}

	seedCompletenessEvidence(t, store, purl, "pg-converge-none", now.Add(time.Hour), true)
	live, err = store.FilterIncompleteAuthoringCandidates(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := axesInRows(live, "pg-converge"); len(got) != 0 {
		t.Fatalf("PostgreSQL complete coordinate still has work: %v", got)
	}

	const evidenceOnlyPURL = "pkg:gem/nokogiri@1.18.10"
	if err := store.UpsertPackage(t.Context(), PackageRow{
		PURL: evidenceOnlyPURL, Ecosystem: "gem", Name: "nokogiri", Version: "1.18.10",
		Major: "1", Publicness: "PUBLIC", LastSeen: now,
	}); err != nil {
		t.Fatal(err)
	}
	seedVerified(t, store, evidenceOnlyPURL, "linux", now)
	rows, err := store.ListAuthoringExpansionCandidates(t.Context(), 200)
	if err != nil {
		t.Fatal(err)
	}
	evidenceOnlyAxes := make(map[string]bool)
	for _, row := range rows {
		if row.Name == "nokogiri" && row.Version == "1.18.10" {
			evidenceOnlyAxes[normalizeAuthoringAxis(row.Axis)] = true
		}
	}
	if len(evidenceOnlyAxes) != 1 || !evidenceOnlyAxes[AuthoringAxisEvidence] {
		t.Fatalf("PostgreSQL verified, unobserved scanner-N/A axes = %v, want Evidence only", evidenceOnlyAxes)
	}
}

func TestIntegrationCompletedSampleAssignmentDoesNotBlockAnotherAxis(t *testing.T) {
	store := openTestPG(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if err := store.IssueAuthoringSessions(t.Context(), []AuthoringSessionRow{
		{TokenHash: "sample-axis-hash", SessionID: "sample-axis-writer", Label: "sample", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour)},
		{TokenHash: "evidence-axis-hash", SessionID: "evidence-axis-writer", Label: "evidence", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour)},
	}, now); err != nil {
		t.Fatal(err)
	}
	sample := WantedRow{Ecosystem: "npm", Name: "pg-completed-axis", Version: "1.0.0", Kind: "WANTED", Axis: AuthoringAxisSample}
	work, found, err := store.ClaimAuthoringWork(t.Context(), "sample-axis-writer", []WantedRow{sample}, now, now.Add(time.Hour))
	if err != nil || !found {
		t.Fatalf("PostgreSQL Sample claim = %+v found=%v err=%v", work, found, err)
	}
	const sampleID = "sha256:pg-completed-axis"
	if err := store.SaveSample(t.Context(), SampleRow{SampleID: sampleID, ManifestJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	if attached, err := store.AttachAuthoringWorkSample(t.Context(), "sample-axis-writer", work, sampleID, now.Add(time.Minute)); err != nil || !attached {
		t.Fatalf("PostgreSQL Sample completion attached=%v err=%v", attached, err)
	}
	evidence := sample
	evidence.Kind = "EXPANSION"
	evidence.Axis = AuthoringAxisEvidence
	next, found, err := store.ClaimAuthoringWork(t.Context(), "evidence-axis-writer", []WantedRow{evidence}, now.Add(2*time.Minute), now.Add(time.Hour))
	if err != nil || !found || next.Axis != AuthoringAxisEvidence {
		t.Fatalf("PostgreSQL completed Sample blocked Evidence: %+v found=%v err=%v", next, found, err)
	}
}

func TestCompletenessSchedulerDoesNotStarveAnyAxisInBoundedWindow(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("sample-%02d", i)
		purl := seedCompletenessPackage(t, store, name, now.Add(time.Duration(i)*time.Minute))
		seedCompletenessEvidence(t, store, purl, name, now, true)
	}

	// An unmeasured release contributes Evidence work.
	seedCompletenessPackage(t, store, "evidence-gap", now.Add(time.Hour))
	// A proven, observed release whose resolver never reported a tree
	// contributes Dependency work and no Sample work.
	dependencyPURL := seedCompletenessPackage(t, store, "dependency-gap", now.Add(2*time.Hour))
	seedCompletenessEvidence(t, store, dependencyPURL, "dependency-gap", now, false)
	seedVerifiedSample(t, store, context.Background(), dependencyPURL, "linux", now)

	rows, err := store.ListAuthoringExpansionCandidates(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, row := range rows {
		got[normalizeAuthoringAxis(row.Axis)] = true
	}
	for _, axis := range []string{AuthoringAxisSample, AuthoringAxisEvidence, AuthoringAxisDependency} {
		if !got[axis] {
			t.Fatalf("bounded window axes = %v rows=%+v; %s starved", got, rows, axis)
		}
	}
}

func TestCompletenessSchedulerEmitsEvidenceOnlyGap(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	const purl = "pkg:gem/nokogiri@1.18.10"
	if err := store.UpsertPackage(t.Context(), PackageRow{
		PURL: purl, Ecosystem: "gem", Name: "nokogiri", Version: "1.18.10",
		Major: "1", Publicness: "PUBLIC", LastSeen: now,
	}); err != nil {
		t.Fatal(err)
	}
	seedVerifiedSample(t, store, context.Background(), purl, "linux", now)
	rows, err := store.ListAuthoringExpansionCandidates(t.Context(), 200)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, row := range rows {
		if row.Name == "nokogiri" {
			got[normalizeAuthoringAxis(row.Axis)] = true
		}
	}
	if len(got) != 1 || !got[AuthoringAxisEvidence] {
		t.Fatalf("verified, unobserved scanner-N/A release axes = %v, want Evidence only", got)
	}
}

func TestCompletenessAxisChangeReleasesLeaseAndRetryGate(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	evidence := WantedRow{Ecosystem: "npm", Name: "axis-change", Version: "1.0.0", Axis: AuthoringAxisEvidence}
	dependency := evidence
	dependency.Axis = AuthoringAxisDependency
	dependency.Kind = "DEPENDENCY"

	first, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", []WantedRow{evidence}, now, now.Add(time.Hour))
	if err != nil || !ok || first.Axis != AuthoringAxisEvidence {
		t.Fatalf("evidence claim = %+v ok=%v err=%v", first, ok, err)
	}
	// The live completeness refresh can replace one axis with another for the
	// same coordinate. The old lease must not survive merely because the
	// coordinate key is unchanged.
	changed, ok, err := store.ClaimAuthoringWork(t.Context(), "writer-a", []WantedRow{dependency}, now.Add(time.Minute), now.Add(time.Hour))
	if err != nil || !ok || changed.Axis != AuthoringAxisDependency {
		t.Fatalf("axis replacement = %+v ok=%v err=%v", changed, ok, err)
	}

	// Two independent writers can establish that Evidence work is impossible
	// without quarantining a separately actionable Dependency deliverable.
	store = NewFake()
	for i, session := range []string{"measure-a", "measure-b"} {
		claimed, found, claimErr := store.ClaimAuthoringWork(t.Context(), session, []WantedRow{evidence}, now.Add(time.Duration(i)*time.Minute), now.Add(time.Hour))
		if claimErr != nil || !found || claimed.Axis != AuthoringAxisEvidence {
			t.Fatalf("measurement %d claim = %+v found=%v err=%v", i, claimed, found, claimErr)
		}
		if _, reported, reportErr := store.ReportAuthoringOutcome(t.Context(), session, AuthoringNoCallableSymbol, "measured", now.Add(time.Duration(i)*time.Minute+time.Second)); reportErr != nil || !reported {
			t.Fatalf("measurement %d report: reported=%v err=%v", i, reported, reportErr)
		}
	}
	if _, found, err := store.ClaimAuthoringWork(t.Context(), "measure-c", []WantedRow{evidence}, now.Add(3*time.Minute), now.Add(time.Hour)); err != nil || found {
		t.Fatalf("impossible Evidence was reissued: found=%v err=%v", found, err)
	}
	if claimed, found, err := store.ClaimAuthoringWork(t.Context(), "dependency-writer", []WantedRow{dependency}, now.Add(4*time.Minute), now.Add(time.Hour)); err != nil || !found || claimed.Axis != AuthoringAxisDependency {
		t.Fatalf("Dependency inherited Evidence quarantine: %+v found=%v err=%v", claimed, found, err)
	}
}

func TestCompletedSampleAssignmentDoesNotBlockAnotherAxis(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	sample := WantedRow{Ecosystem: "npm", Name: "completed-axis", Version: "1.0.0", Kind: "WANTED", Axis: AuthoringAxisSample}
	work, found, err := store.ClaimAuthoringWork(t.Context(), "sample-writer", []WantedRow{sample}, now, now.Add(time.Hour))
	if err != nil || !found {
		t.Fatalf("Sample claim = %+v found=%v err=%v", work, found, err)
	}
	if attached, err := store.AttachAuthoringWorkSample(t.Context(), "sample-writer", work, "sha256:completed-axis", now.Add(time.Minute)); err != nil || !attached {
		t.Fatalf("Sample completion attached=%v err=%v", attached, err)
	}

	evidence := sample
	evidence.Kind = "EXPANSION"
	evidence.Axis = AuthoringAxisEvidence
	next, found, err := store.ClaimAuthoringWork(t.Context(), "evidence-writer", []WantedRow{evidence}, now.Add(2*time.Minute), now.Add(time.Hour))
	if err != nil || !found || next.Axis != AuthoringAxisEvidence {
		t.Fatalf("completed Sample blocked Evidence: %+v found=%v err=%v", next, found, err)
	}
}

func TestCompletenessEvidencePriorityIncludesExplicitDemand(t *testing.T) {
	store := NewFake()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	store.NowFn = func() time.Time { return now }
	seedCompletenessPackage(t, store, "frontier-only", now.Add(time.Hour))
	seedCompletenessPackage(t, store, "user-demand", now)
	for i := 0; i < 3; i++ {
		if err := store.RecordWanted(t.Context(), now.Format("2006-01-02"), fmt.Sprintf("anon-demand-%d", i), []WantedRow{
			{Ecosystem: "npm", Name: "user-demand", Version: "1.0.0"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListAuthoringExpansionCandidates(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "user-demand" || rows[0].Axis != AuthoringAxisEvidence || rows[0].Score != 3000 {
		t.Fatalf("evidence priority = %+v, want explicit demand first", rows)
	}
}
