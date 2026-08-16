package search

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

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

	// The union input is now safe from two directions: the grader is given
	// the sample's DECLARED packages, and even handed the union it takes
	// the WORST shared pair rather than the friendliest — so the version
	// the sample never declared can no longer produce an exact claim.
	if rel, _, _ := packageRelation(req, union); rel == relExactVersion {
		t.Errorf("the shard union still grades EXACT: %v", rel)
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

func TestGradePackagesPreferActualResolverOutputAndRetainDeclaredFallback(t *testing.T) {
	c := &candidate{
		declared: parsePURLs([]string{"pkg:npm/axios@1.19.0"}),
		verifications: []verificationVariant{{
			packages: parsePURLs([]string{"pkg:npm/axios@1.12.4"}),
			stages:   map[string]string{"resolve": "PASS", "contract": "PASS"},
		}},
	}
	selection := selectGradeVariant(c, nil,
		parsePURLs([]string{"pkg:npm/axios@1.12.4"}), domain.EnvironmentFingerprint{})
	if selection.rel != relExactVersion || selection.samP.Version != "1.12.4" {
		t.Fatalf("grade selection = %+v, want resolved 1.12.4", selection)
	}
	if c.declared[0].Version != "1.19.0" {
		t.Fatalf("selecting verified packages rewrote the declared input: %v", c.declared)
	}
}

func TestV1OrEmptyResolvedListNeverEstablishesASearchVersion(t *testing.T) {
	c := &candidate{declared: parsePURLs([]string{"pkg:npm/axios@1.19.0"})}
	v1 := domain.VerificationReceipt{
		SchemaVersion:    1,
		Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
		ResolvedPackages: []string{"pkg:npm/axios@1.12.4"},
	}
	asked := parsePURLs([]string{"pkg:npm/axios@1.12.4"})
	selection := selectGradeVariant(c, []domain.VerificationReceipt{v1}, asked, domain.EnvironmentFingerprint{})
	if selection.rel != relPackageOnly {
		t.Fatalf("v1 relation = %v, want package-only", selection.rel)
	}

	v2Empty := v1
	v2Empty.SchemaVersion = 2
	v2Empty.ResolvedPackages = nil
	selection = selectGradeVariant(c, []domain.VerificationReceipt{v2Empty}, asked, domain.EnvironmentFingerprint{})
	if selection.rel != relPackageOnly {
		t.Fatalf("empty v2 relation = %v, want package-only", selection.rel)
	}

	v2 := v2Empty
	v2.ResolvedPackages = []string{"pkg:npm/axios@1.12.4"}
	selection = selectGradeVariant(c, []domain.VerificationReceipt{v2}, asked, domain.EnvironmentFingerprint{})
	if selection.rel != relExactVersion || selection.samP.Version != "1.12.4" {
		t.Fatalf("v2 grade selection = %+v", selection)
	}
}

func TestMalformedResolvedListFailsClosedForSearchGrading(t *testing.T) {
	receipt := domain.VerificationReceipt{
		SchemaVersion: 2,
		Stages:        map[string]string{"resolve": "PASS"},
		ResolvedPackages: []string{
			"pkg:npm/axios@1.12.4",
			"pkg:npm/axios@^1",
		},
	}
	if got := verifiedPURLsFromReceipt(receipt); len(got) != 0 {
		t.Fatalf("partially accepted a malformed resolver claim: %v", got)
	}
}

func TestReceiptVerdictStaysScopedToItsResolvedVersion(t *testing.T) {
	env := nodeEnv("cjs")
	c := &candidate{
		declared: parsePURLs([]string{"pkg:npm/axios@^1"}),
		verifications: []verificationVariant{
			{packages: parsePURLs([]string{"pkg:npm/axios@1.12.4"}), env: env,
				stages: map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "FAIL"}},
			{packages: parsePURLs([]string{"pkg:npm/axios@2.0.0"}), env: env,
				stages: map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"}, level: 3},
		},
	}
	failing := selectGradeVariant(c, nil, parsePURLs([]string{"pkg:npm/axios@1.12.4"}), env)
	if failing.rel != relExactVersion || failing.stages["contract"] != "FAIL" || failing.level != 0 {
		t.Fatalf("axios 1 selection borrowed axios 2 PASS: %+v", failing)
	}
	passing := selectGradeVariant(c, nil, parsePURLs([]string{"pkg:npm/axios@2.0.0"}), env)
	if passing.rel != relExactVersion || passing.stages["contract"] != "PASS" || passing.level < 3 {
		t.Fatalf("axios 2 selection lost its PASS: %+v", passing)
	}
}

func TestReceiptVariantsDoNotInventCrossReceiptPackageCombination(t *testing.T) {
	env := nodeEnv("cjs")
	c := &candidate{
		declared: parsePURLs([]string{"pkg:npm/axios@^1", "pkg:npm/zod@^1"}),
		verifications: []verificationVariant{
			{packages: parsePURLs([]string{"pkg:npm/axios@1.0.0", "pkg:npm/zod@1.0.0"}), env: env,
				stages: map[string]string{"resolve": "PASS", "contract": "PASS"}},
			{packages: parsePURLs([]string{"pkg:npm/axios@2.0.0", "pkg:npm/zod@2.0.0"}), env: env,
				stages: map[string]string{"resolve": "PASS", "contract": "PASS"}},
		},
	}
	selection := selectGradeVariant(c, nil,
		parsePURLs([]string{"pkg:npm/axios@1.0.0", "pkg:npm/zod@2.0.0"}), env)
	if selection.rel == relExactVersion {
		t.Fatalf("invented an exact combination no receipt ran: %+v", selection)
	}
}

func TestPartiallyResolvedReceiptRetainsDeclaredPackageIdentity(t *testing.T) {
	env := nodeEnv("cjs")
	c := &candidate{
		declared: parsePURLs([]string{"pkg:npm/axios@^1", "pkg:npm/zod@^3"}),
		verifications: []verificationVariant{{
			packages: parsePURLs([]string{"pkg:npm/axios@1.12.4"}), env: env,
			stages: map[string]string{"resolve": "PASS", "contract": "PASS"},
		}},
	}
	selection := selectGradeVariant(c, nil, parsePURLs([]string{"pkg:npm/zod@3.2.1"}), env)
	if selection.rel != relPackageOnly || selection.samP.Name != "zod" {
		t.Fatalf("unresolved declared zod disappeared from grading: %+v", selection)
	}
}

func TestShardSearchGradesResolvedVersionButDisplaysDeclaredVersion(t *testing.T) {
	db := openDB(t)
	env := nodeEnv("cjs")
	sample := shardSampleEntry{
		SampleID: "sha256:resolved-wire", Goal: "post JSON with axios",
		Status: "CROSS_PASS", License: "MIT-0",
		Packages:    []string{"pkg:npm/axios@1.19.0"},
		Environment: env,
		Verifications: []shardVerificationEntry{{
			ResolvedPackages: []string{"pkg:npm/axios@1.12.4"}, Environment: env,
			Stages: map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
		}},
	}
	saveShardJSON(t, db, "npm/axios/1", shardFile{
		SchemaVersion: 1, Key: "npm/axios/1",
		Packages: []shardPackage{{PURL: "pkg:npm/axios@1.12.4", Samples: []shardSampleEntry{sample}}},
	})
	resp := Engine{DB: db}.Search(context.Background(), domain.SearchRequest{
		SchemaVersion: 1, Query: "post JSON with axios",
		Packages: []string{"pkg:npm/axios@1.12.4"}, Environment: env,
	})
	if resp.Miss || len(resp.Results) != 1 {
		t.Fatalf("resolved shard sample missed: %+v", resp)
	}
	result := resp.Results[0]
	if result.Grade != domain.GradeExact {
		t.Fatalf("grade = %s, want EXACT from resolved version", result.Grade)
	}
	if result.Case == nil || len(result.Case.Packages) != 1 ||
		result.Case.Packages[0] != "pkg:npm/axios@1.19.0" {
		t.Fatalf("declared package was not retained for display: %+v", result.Case)
	}
}

func TestShardSearchDoesNotBorrowVersionFromDeclaredOrEnclosingPURL(t *testing.T) {
	db := openDB(t)
	env := nodeEnv("cjs")
	saveShardJSON(t, db, "npm/axios/1", shardFile{
		SchemaVersion: 1, Key: "npm/axios/1",
		Packages: []shardPackage{{
			PURL: "pkg:npm/axios@1.12.4",
			Samples: []shardSampleEntry{{
				SampleID: "sha256:unestablished-wire", Goal: "post JSON with axios",
				Status: "CROSS_PASS", License: "MIT-0",
				Packages: []string{"pkg:npm/axios@1.12.4"}, Environment: env,
			}},
		}},
	})
	resp := Engine{DB: db}.Search(context.Background(), domain.SearchRequest{
		SchemaVersion: 1, Query: "post JSON with axios",
		Packages: []string{"pkg:npm/axios@1.12.4"}, Environment: env,
	})
	if resp.Miss || len(resp.Results) != 1 {
		t.Fatalf("unestablished shard sample missed: %+v", resp)
	}
	result := resp.Results[0]
	if result.Grade == domain.GradeExact {
		t.Fatal("manifest/shard purl produced an exact version grade without resolver evidence")
	}
	if len(result.Different) == 0 {
		t.Fatalf("unestablished version was not disclosed: %+v", result)
	}
}
