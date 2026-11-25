package serverstore

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestValidateBuildDiagnostics(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	ok := domain.BuildDiagnostic{Tool: "go/vet", ToolVersion: "go1.26.5", Code: "SA1019",
		Severity: domain.BuildDiagnosticWarning, Kind: domain.BuildDiagnosticDeprecated, Stage: "compile",
		Message: `"io/ioutil" has been deprecated since Go 1.19`, File: "main.go", Line: 3, RawLogDigest: digest}
	if err := ValidateBuildDiagnostics(nil, true); err != nil {
		t.Fatalf("absent diagnostics rejected: %v", err)
	}
	if err := ValidateBuildDiagnostics([]domain.BuildDiagnostic{ok}, true); err != nil {
		t.Fatalf("valid receipt diagnostic rejected: %v", err)
	}
	anon := ok
	anon.File, anon.Line = "", 0
	if err := ValidateBuildDiagnostics([]domain.BuildDiagnostic{anon}, false); err != nil {
		t.Fatalf("valid observation diagnostic rejected: %v", err)
	}
	bad := map[string]func(d *domain.BuildDiagnostic){
		"tool":     func(d *domain.BuildDiagnostic) { d.Tool = "Go Vet" },
		"severity": func(d *domain.BuildDiagnostic) { d.Severity = "error" },
		"kind":     func(d *domain.BuildDiagnostic) { d.Kind = "" },
		"message":  func(d *domain.BuildDiagnostic) { d.Message = `C:\Users\me\proj\main.go: deprecated` },
		"empty":    func(d *domain.BuildDiagnostic) { d.Message = "" },
		"file":     func(d *domain.BuildDiagnostic) { d.File = "/home/me/main.go" },
		"line":     func(d *domain.BuildDiagnostic) { d.File = "" },
		"digest":   func(d *domain.BuildDiagnostic) { d.RawLogDigest = "deadbeef" },
		"code":     func(d *domain.BuildDiagnostic) { d.Code = strings.Repeat("x", 65) },
	}
	for name, mutate := range bad {
		d := ok
		mutate(&d)
		if err := ValidateBuildDiagnostics([]domain.BuildDiagnostic{d}, true); err == nil {
			t.Errorf("%s: invalid diagnostic accepted: %+v", name, d)
		}
	}
	if err := ValidateBuildDiagnostics([]domain.BuildDiagnostic{ok}, false); err == nil {
		t.Error("file name accepted on an anonymous observation")
	}
	if err := ValidateBuildDiagnostics([]domain.BuildDiagnostic{}, true); err == nil {
		t.Error("present-empty diagnostics accepted")
	}
	many := make([]domain.BuildDiagnostic, domain.MaxBuildDiagnostics+1)
	for i := range many {
		many[i] = ok
	}
	if err := ValidateBuildDiagnostics(many, true); err == nil {
		t.Error("over-cap diagnostics accepted")
	}
}

func TestValidateBatchDiagnosticsByVersion(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	b := domain.ObservationBatch{SchemaVersion: 2, Epoch: "2026-11-25", AnonID: "0123456789abcdef", ProjectBucket: "0123456789ab",
		Package:     "pkg:npm/axios@1.12.0",
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64"},
		Stage:       domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
		Diagnostics: []domain.BuildDiagnostic{{Tool: "typescript", Severity: domain.BuildDiagnosticWarning,
			Kind: domain.BuildDiagnosticDeprecated, Message: "'substr' is deprecated.", RawLogDigest: digest}}}
	if err := ValidateBatch(b); err != nil {
		t.Fatalf("v2 batch with diagnostics rejected: %v", err)
	}
	b.SchemaVersion = 1
	if err := ValidateBatch(b); err == nil {
		t.Fatal("v1 batch carrying diagnostics accepted")
	}
	b.Diagnostics = nil
	if err := ValidateBatch(b); err != nil {
		t.Fatalf("v1 batch without diagnostics rejected: %v", err)
	}
}
