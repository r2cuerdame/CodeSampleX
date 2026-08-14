package search

import "testing"

// A shard lists one sample under every package version it is relevant to.
// The candidate therefore accumulates purls it was never verified against,
// and grading off that union reported an EXACT match on a version the
// sample does not declare: MCP search answered "MATCH: EXACT, Exact: axios
// 1.12" for a sample whose own csx.json pins axios@1.19.0, while the server
// graded the same input ADAPTATION_REQUIRED. A wrong HIT is worse than a
// MISS, and this one arrived with the highest confidence the system has.
func TestGradeUsesTheSamplesOwnPackagesNotTheShardKey(t *testing.T) {
	req := parsePURLs([]string{"pkg:npm/axios@1.12.2"})
	declared := parsePURLs([]string{"pkg:npm/axios@1.19.0"})
	// What the shard union used to hand the grader.
	union := parsePURLs([]string{"pkg:npm/axios@1.12.2", "pkg:npm/axios@1.19.0"})

	if rel, _, _ := packageRelation(req, union); rel != relExactVersion {
		t.Fatalf("union relation = %v; the test is not reproducing the old input", rel)
	}
	rel, _, samP := packageRelation(req, declared)
	if rel == relExactVersion {
		t.Error("graded an exact version match against a version the sample does not declare")
	}
	if rel != relMajorMinor && rel != relMajor {
		t.Errorf("relation = %v, want a version difference to be reported", rel)
	}
	if samP.Version != "1.19.0" {
		t.Errorf("reported sample package %q, want the declared 1.19.0", samP.Version)
	}
}

// When a shard predates the packages field there is nothing authoritative to
// grade against, so the ceiling is the package name. Anything above it would
// be a claim about a version nothing established.
func TestPackageOnlyRelationCannotReachExact(t *testing.T) {
	if relPackageOnly >= relMajor {
		t.Fatal("relPackageOnly must rank below every claim about a version")
	}
	if relPackageOnly <= relMajorDiff {
		t.Fatal("relPackageOnly must rank above a known major difference")
	}
	grade, _ := buildGrade(relPackageOnly, nil, contextDelta{}, false)
	if grade == "EXACT" {
		t.Error("a candidate with no established version was graded EXACT")
	}
}

// The delta must not print a version it did not verify, in either direction.
func TestDeltaDoesNotPrintAnUnestablishedVersion(t *testing.T) {
	req := parsePURLs([]string{"pkg:npm/axios@1.12.2"})[0]
	sam := parsePURLs([]string{"pkg:npm/axios@1.19.0"})[0]

	exact, different := buildDelta(relPackageOnly, req, sam, nil, contextDelta{})
	for _, e := range exact {
		t.Errorf("claimed %q as exact with no established version", e)
	}
	if len(different) == 0 {
		t.Fatal("said nothing at all about the package")
	}
}
