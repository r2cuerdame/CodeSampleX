package serverstore

import (
	"context"
	"strings"
	"testing"
)

// Migration 0024 stopped clearing failure_clusters, because clearing a
// production table is a destructive operation that does not belong in an
// unattended additive migration. The rebuilt rows therefore land BESIDE the
// pre-0024 rows: production went from 17,737 to 35,488 cluster observations
// on one deployment while the underlying FAIL total never moved.
//
// PostgreSQL now hides the preserved rows behind
// CurrentFailureClusterPredicateSQL. The Fake did not, so every handler and
// builder test kept seeing a world PostgreSQL no longer serves — which is
// exactly the divergence that let the doubling reach production with a green
// suite.
func TestFakeHidesPreservedLegacyClustersLikePostgreSQL(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	preserved := ClusterRow{
		Ecosystem: "golang", PackageName: "github.com/jackc/pgx/v5", Symbol: "ParseConfig",
		Stage: "PROJECT_TEST", ErrorFingerprint: "sha256:" + strings.Repeat("a", 64),
		EvidenceQuality: "legacy-evidence-incomplete", ObservationCount: 408,
	}
	rebuilt := preserved
	rebuilt.ErrorFingerprint = ""
	rebuilt.ObservationCount = 482

	for _, row := range []ClusterRow{preserved, rebuilt} {
		if err := f.UpsertFailureCluster(ctx, row); err != nil {
			t.Fatalf("upsert %q: %v", row.ErrorFingerprint, err)
		}
	}

	got, err := f.ListFailureClusters(ctx, preserved.PackageName)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ErrorFingerprint != "" || got[0].ObservationCount != rebuilt.ObservationCount {
		t.Fatalf("current clusters = %+v, want only the rebuilt evidence-gap row", got)
	}
	// Hidden, never deleted: the preserved row is historical material and a
	// separately authorized cleanup is the only thing allowed to remove it.
	if len(f.clusters) != 2 {
		t.Fatalf("stored rows = %d, want the preserved legacy row kept beside the rebuilt one", len(f.clusters))
	}
}

// Hiding the preserved rows from every read would have taken exact failure
// matching down with them. Every released client fingerprints a failure as
// `v1|stage|code|template`, so every fingerprint on file lives on a preserved
// row; the rebuilt evidence-gap rows carry no fingerprint at all and can
// never match anything. The two reads answer two different questions and must
// keep disagreeing about these rows.
func TestPreservedFingerprintsStayExactMatchableWhileLeavingTheCurrentReads(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	const pkg = "axios"
	preserved := ClusterRow{
		Ecosystem: "npm", PackageName: pkg, Symbol: "axios.post", Stage: "PROJECT_COMPILE",
		ErrorFingerprint: "sha256:" + strings.Repeat("ab", 32),
		EvidenceQuality:  "legacy-evidence-incomplete", ObservationCount: 7,
	}
	if err := f.UpsertFailureCluster(ctx, preserved); err != nil {
		t.Fatal(err)
	}

	current, err := f.ListFailureClusters(ctx, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 0 {
		t.Errorf("current clusters = %+v, want the preserved row withheld", current)
	}

	recorded, err := f.ListFailureClustersIncludingPreserved(ctx, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || recorded[0].ErrorFingerprint != preserved.ErrorFingerprint {
		t.Fatalf("recorded clusters = %+v, want the preserved fingerprint still matchable", recorded)
	}
}

// The other place PostgreSQL restricts itself to current clusters: FINDING
// work is ranked by a cluster's observation count, so a preserved pre-0024
// row would send the farm after a failure identity the builder stopped
// writing — and it would outrank live work, because its count is the same
// failures counted a second time.
func TestPreservedLegacyClustersDoNotRankAuthoringWork(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	const purl = "pkg:npm/left-pad@1.0.0"
	if err := f.UpsertPackage(ctx, PackageRow{PURL: purl, Ecosystem: "npm",
		Name: "left-pad", Version: "1.0.0", Publicness: "PUBLIC"}); err != nil {
		t.Fatal(err)
	}
	if err := f.UpsertFailureCluster(ctx, ClusterRow{
		Ecosystem: "npm", PackageName: "left-pad", Symbol: "leftPad", Stage: "PROJECT_TEST",
		ErrorFingerprint: "sha256:" + strings.Repeat("a", 64),
		EvidenceQuality:  "legacy-evidence-incomplete",
		ObservationCount: 900, VersionsJSON: `["1.0.0"]`,
	}); err != nil {
		t.Fatal(err)
	}

	candidates, err := f.ListAuthoringExpansionCandidates(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.Kind == "FINDING" {
			t.Fatalf("a preserved pre-0024 cluster produced FINDING work: %+v", c)
		}
	}
}

// IsCurrentFailureCluster is the Go twin of
// CurrentFailureClusterPredicateSQL. Both must agree that a collapsed
// evidence-gap row is live and a pre-contract fingerprint is not.
func TestIsCurrentFailureClusterSeparatesLiveRowsFromPreservedOnes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		row     ClusterRow
		current bool
	}{
		{"rebuilt evidence gap", ClusterRow{EvidenceQuality: "legacy-evidence-incomplete"}, true},
		{"modern complete", ClusterRow{EvidenceQuality: "complete", ErrorFingerprint: "sha256:" + strings.Repeat("b", 64)}, true},
		{"modern partial", ClusterRow{EvidenceQuality: "partial", ErrorFingerprint: "sha256:" + strings.Repeat("c", 64)}, true},
		{"preserved legacy fingerprint", ClusterRow{EvidenceQuality: "legacy-evidence-incomplete", ErrorFingerprint: "sha256:" + strings.Repeat("d", 64)}, false},
		{"preserved missing-quality fingerprint", ClusterRow{EvidenceQuality: "missing", ErrorFingerprint: "sha256:" + strings.Repeat("e", 64)}, false},
		// A row written before the column existed reads back as the
		// PostgreSQL default, so an empty quality must behave like it.
		{"pre-0024 row with no quality", ClusterRow{ErrorFingerprint: "sha256:" + strings.Repeat("f", 64)}, false},
	} {
		if got := IsCurrentFailureCluster(tc.row); got != tc.current {
			t.Errorf("%s: IsCurrentFailureCluster = %v, want %v", tc.name, got, tc.current)
		}
	}
}
