package serverstore

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestValidateBatchAccepts(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*domain.ObservationBatch)
	}{
		{"baseline npm", func(b *domain.ObservationBatch) {}},
		{"golang multi-segment purl", func(b *domain.ObservationBatch) {
			b.Package = "pkg:golang/github.com/jackc/pgx/v5@v5.10.0"
			b.Symbol = "github.com/jackc/pgx/v5.Connect"
		}},
		{"scoped npm raw form", func(b *domain.ObservationBatch) {
			b.Package = "pkg:npm/@scope/pkg@1.0.0"
		}},
		{"package-only usage, no symbol", func(b *domain.ObservationBatch) {
			b.Symbol = ""
			b.SymbolConfidence = ""
		}},
		{"failure with fingerprint and code", func(b *domain.ObservationBatch) {
			b.Result = domain.ResultFail
			b.ErrorFingerprint = "sha256:" + strings.Repeat("ab", 32)
			b.ErrorCode = "ERR_REQUIRE_ESM"
		}},
		{"UNKNOWN symbol confidence", func(b *domain.ObservationBatch) {
			b.SymbolConfidence = domain.SymbolUnknown
		}},
		{"PROJECT_LOAD stage", func(b *domain.ObservationBatch) {
			b.Stage = domain.StageProjectLoad
		}},
		{"USED stage", func(b *domain.ObservationBatch) {
			b.Stage = domain.StageUsed
		}},
	}
	for _, tc := range cases {
		b := obsBatch("anonaaaa", "projaaaa", 3)
		tc.mut(&b)
		if err := ValidateBatch(b); err != nil {
			t.Errorf("%s: ValidateBatch rejected valid batch: %v", tc.name, err)
		}
	}
}

func TestValidateBatchRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*domain.ObservationBatch)
		want string // substring expected in the reason
	}{
		{"schema version 0", func(b *domain.ObservationBatch) { b.SchemaVersion = 0 }, "schemaVersion"},
		{"bad purl", func(b *domain.ObservationBatch) { b.Package = "axios@1.12.0" }, "purl"},
		{"ecosystem not allowlisted", func(b *domain.ObservationBatch) { b.Package = "pkg:nuget/Newtonsoft.Json@13.0.0" }, "ecosystem"},
		{"SYMBOL_EXECUTED is A3-only", func(b *domain.ObservationBatch) { b.Stage = domain.StageSymbolExecuted }, "A3"},
		{"SYMBOL_CALL is A3-only", func(b *domain.ObservationBatch) { b.Stage = domain.StageSymbolCall }, "A3"},
		{"verification stage CONTRACT not an observation", func(b *domain.ObservationBatch) { b.Stage = domain.StageContract }, "stage"},
		{"made-up stage", func(b *domain.ObservationBatch) { b.Stage = "COMPILED_GREAT" }, "stage"},
		{"bad result", func(b *domain.ObservationBatch) { b.Result = "MAYBE" }, "result"},
		{"bad symbol confidence", func(b *domain.ObservationBatch) { b.SymbolConfidence = "CERTAIN" }, "symbolConfidence"},
		{"zero count", func(b *domain.ObservationBatch) { b.ObservationCount = 0 }, "observationCount"},
		{"negative count", func(b *domain.ObservationBatch) { b.ObservationCount = -1 }, "observationCount"},
		{"absurd count", func(b *domain.ObservationBatch) { b.ObservationCount = 2_000_000 }, "observationCount"},
		{"empty epoch", func(b *domain.ObservationBatch) { b.Epoch = "" }, "epoch"},
		{"non-date epoch", func(b *domain.ObservationBatch) { b.Epoch = "yesterday" }, "epoch"},
		{"empty anon id", func(b *domain.ObservationBatch) { b.AnonID = "" }, "anonId"},
		{"empty project bucket", func(b *domain.ObservationBatch) { b.ProjectBucket = "" }, "projectBucket"},
		{"whitespace in anon id", func(b *domain.ObservationBatch) { b.AnonID = "a b" }, "anonId"},
		{"raw text in errorFingerprint", func(b *domain.ObservationBatch) {
			b.ErrorFingerprint = "Cannot find module 'C:\\proj\\secret.js'"
		}, "errorFingerprint"},
		{"error code with spaces (raw log)", func(b *domain.ObservationBatch) {
			b.ErrorCode = "error TS2345: Argument of type"
		}, "errorCode"},
		{"env missing ecosystem", func(b *domain.ObservationBatch) { b.Environment.Ecosystem = "" }, "environment"},
		{"env missing os", func(b *domain.ObservationBatch) { b.Environment.OS = "" }, "environment"},
		{"env missing arch", func(b *domain.ObservationBatch) { b.Environment.Arch = "" }, "environment"},
		{"env wrong schema version", func(b *domain.ObservationBatch) { b.Environment.SchemaVersion = 2 }, "environment"},
	}
	for _, tc := range cases {
		b := obsBatch("anonaaaa", "projaaaa", 3)
		tc.mut(&b)
		err := ValidateBatch(b)
		if err == nil {
			t.Errorf("%s: ValidateBatch accepted invalid batch", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: reason %q does not mention %q", tc.name, err.Error(), tc.want)
		}
	}
}
