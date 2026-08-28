package search

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestDebugTraceDoesNotChangeSearchSemantics(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("upload multipart form with axios",
		[]string{"pkg:npm/axios@1.12.0"}, nodeEnv("esm"), "axios.post")
	if err := SeedSampleDoc(ctx, db, m, "sha256:debugsame", "LOCAL_PASS"); err != nil {
		t.Fatal(err)
	}
	saveResolvedReceipt(t, db, "sha256:debugsame", m, "ed25519:debug")
	req := domain.SearchRequest{
		SchemaVersion: 2, Query: "upload multipart form with axios",
		Packages: []string{"pkg:npm/axios@1.12.0"}, Symbols: []string{"axios.post"},
		Environment: nodeEnv("esm"),
	}
	off := Engine{DB: db}.Search(ctx, req)
	req.Debug = true
	on := Engine{DB: db}.Search(ctx, req)
	if on.Diagnostic == nil || len(on.Diagnostic.Pipeline) < 4 {
		t.Fatalf("debug pipeline missing: %+v", on.Diagnostic)
	}
	on.Diagnostic = nil
	if !reflect.DeepEqual(off, on) {
		a, _ := json.Marshal(off)
		b, _ := json.Marshal(on)
		t.Fatalf("debug changed result semantics\noff=%s\non=%s", a, b)
	}
}

func TestNoSafeMatchTraceCarriesActualRejectionReasonAndGaps(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	m := mkManifest("format integers with thousand separators",
		[]string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"},
		domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", Runtime: "go", RuntimeVersion: "1.26", OS: "linux", Arch: "amd64"},
		"humanize.FormatInteger")
	if err := SeedSampleDoc(ctx, db, m, "sha256:unrelateddebug", "LOCAL_PASS"); err != nil {
		t.Fatal(err)
	}
	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 2, Debug: true,
		Query:       "GitHub Actions workflow_dispatch deploy immutable main SHA",
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", Runtime: "go", RuntimeVersion: "1.26", OS: "windows", Arch: "amd64"},
	})
	if !resp.Miss || resp.Diagnostic == nil || resp.Diagnostic.Decision != string(domain.GradeNoSafeMatch) {
		t.Fatalf("expected diagnosed miss: %+v", resp)
	}
	if len(resp.Diagnostic.Candidates) != 1 || !hasReason(resp.Diagnostic.Candidates[0].ReasonCodes, domain.SuppressedInsufficientGoalOverlap) {
		t.Fatalf("actual rejection reason missing: %+v", resp.Diagnostic.Candidates)
	}
	if !hasGap(resp.Diagnostic.Gaps, "S", "NO_SAFE_MATCH") || !hasGap(resp.Diagnostic.Gaps, "D", "DEPENDENCY_GRAPH") {
		t.Fatalf("canonical gaps missing: %+v", resp.Diagnostic.Gaps)
	}
}

func TestExactVersionEnvironmentMismatchIsVisibleForAdaptationCandidate(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	sampleEnv := nodeEnv("esm")
	m := mkManifest("upload multipart form with axios",
		[]string{"pkg:npm/axios@1.12.0"}, sampleEnv, "axios.post")
	if err := SeedSampleDoc(ctx, db, m, "sha256:envdebug", "DRAFT"); err != nil {
		t.Fatal(err)
	}
	reqEnv := sampleEnv
	reqEnv.ExecutionContext = "browser"
	reqEnv.BrowserFamily = "safari"
	reqEnv.BrowserMajor = "19"
	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 2, Debug: true, Query: "upload multipart form with axios",
		Packages: []string{"pkg:npm/axios@1.12.0"}, Environment: reqEnv,
	})
	if resp.Miss || len(resp.Results) != 1 || len(resp.Diagnostic.Candidates) != 1 {
		t.Fatalf("expected diagnosed adaptation candidate: %+v", resp)
	}
	candidate := resp.Diagnostic.Candidates[0]
	if candidate.Outcome != "selected" {
		t.Fatalf("exact package candidate should remain selectable with an adaptation delta: %+v", candidate)
	}
	reasons := candidate.ReasonCodes
	if !hasReason(reasons, "environment-mismatch") || !hasReason(reasons, "evidence-scope-insufficient") {
		t.Fatalf("environment/evidence decision lineage missing: %v", reasons)
	}
}

func hasReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func hasGap(gaps []domain.DiagnosticGap, code, reason string) bool {
	for _, gap := range gaps {
		if gap.Code == code && gap.ReasonCode == reason {
			return true
		}
	}
	return false
}
