package compatibility

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestFailureClustersKeepEvidenceGapsAndAggregateEnvironmentVariants(t *testing.T) {
	now := time.Date(2026, 8, 24, 6, 0, 0, 0, time.UTC)
	fp := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rows := map[string][]serverstore.EvidenceRow{
		"v5.10.0": {
			failureEvidenceRow("windows", fp, domain.TerminationExit, domain.EvidenceComplete, 38, now),
			failureEvidenceRow("linux", fp, domain.TerminationExit, domain.EvidenceComplete, 4, now),
			failureEvidenceRow("windows", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "", domain.EvidenceLegacyIncomplete, 185, now),
		},
	}
	clusters := BuildClusters("golang", "github.com/jackc/pgx/v5", rows, nil, now)
	if len(clusters) != 2 {
		t.Fatalf("clusters = %d, want one fingerprint plus one evidence gap: %+v", len(clusters), clusters)
	}
	var fingerprinted, gap serverstore.ClusterRow
	for _, c := range clusters {
		if c.EvidenceQuality == string(domain.EvidenceLegacyIncomplete) {
			gap = c
		} else {
			fingerprinted = c
		}
	}
	if fingerprinted.ObservationCount != 42 || fingerprinted.TerminationKind != string(domain.TerminationExit) {
		t.Errorf("fingerprinted cluster = %+v", fingerprinted)
	}
	var variants []domain.FailureEnvironmentVariant
	if err := json.Unmarshal([]byte(fingerprinted.EnvVariantsJSON), &variants); err != nil || len(variants) != 2 {
		t.Fatalf("environment variants = %s err=%v", fingerprinted.EnvVariantsJSON, err)
	}
	if gap.EvidenceQuality != string(domain.EvidenceLegacyIncomplete) || !gap.DiagnosticCandidate {
		t.Errorf("legacy evidence gap was not exposed/reverification-ranked: %+v", gap)
	}
	if gap.ErrorFingerprint != "" {
		t.Errorf("legacy hash was exposed as a modern cluster identity: %+v", gap)
	}
}

func TestPGXFixtureFailureBreakdownAddsUpWithoutInventingCauses(t *testing.T) {
	now := time.Date(2026, 8, 24, 6, 0, 0, 0, time.UTC)
	env := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", OS: "windows", Arch: "amd64", Runtime: "go", RuntimeVersion: "1.26"}.Normalize()
	rows := []serverstore.EvidenceRow{
		{EnvHash: env.Hash(), EnvJSON: string(domain.MustCanonicalJSON(env)), Stage: "PROJECT_TEST", Result: "PASS", ObservationCount: 1025, LastSeen: now},
		{EnvHash: env.Hash(), EnvJSON: string(domain.MustCanonicalJSON(env)), Stage: "PROJECT_TEST", Result: "FAIL", ObservationCount: 227, EvidenceQuality: string(domain.EvidenceLegacyIncomplete), LastSeen: now},
	}
	snap := BuildSnapshot("pkg:golang/github.com/jackc/pgx/v5@v5.10.0", "ParseConfig", rows, nil, nil, now)
	sc := snap.Rows[0].ByStage["PROJECT_TEST"]
	if sc.Pass != 1025 || sc.Fail != 227 || sc.FailLegacyIncomplete != 227 {
		t.Fatalf("pgx fixture breakdown = %+v", sc)
	}
	if sc.FailComplete+sc.FailPartial+sc.FailMissing+sc.FailLegacyIncomplete != sc.Fail {
		t.Fatalf("quality breakdown does not add up to failures: %+v", sc)
	}
}

func TestFailureClusterPreservesActualStageLineageAndDiagnosticGap(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	exitCode := 1
	env := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", OS: "windows", Arch: "amd64", Runtime: "go", RuntimeVersion: "1.26"}.Normalize()
	rows := map[string][]serverstore.EvidenceRow{"v1.0.0": {{
		PURL: "pkg:golang/example.com/stagefixture@v1.0.0", EnvHash: env.Hash(), EnvJSON: string(domain.MustCanonicalJSON(env)),
		Stage: "PROJECT_COMPILE", Result: "FAIL", ErrorFingerprint: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		TerminationKind: string(domain.TerminationExit), ExitCode: &exitCode, EvidenceQuality: string(domain.EvidencePartial),
		OuterCommand: "go test", OuterStage: "PROJECT_TEST", ActualToolchain: "go/compiler",
		StageEvidence: string(domain.FailureStageBuildAggregate), FailureEvidenceGap: string(domain.FailureDiagnosticMissing),
		ObservationCount: 1, FirstSeen: now, LastSeen: now,
	}}}
	clusters := BuildClusters("golang", "example.com/stagefixture", rows, nil, now)
	if len(clusters) != 1 {
		t.Fatalf("clusters = %+v", clusters)
	}
	got := clusters[0]
	if got.Stage != "PROJECT_COMPILE" || got.ActualToolchain != "go/compiler" || got.StageEvidence != string(domain.FailureStageBuildAggregate) ||
		got.FailureEvidenceGap != string(domain.FailureDiagnosticMissing) || !got.DiagnosticCandidate || len(got.OuterCommands) != 1 || got.OuterCommands[0] != "go test" {
		t.Fatalf("lineage cluster = %+v", got)
	}
}

func TestFailureClustersUnionEveryOuterCommandFromOneEvidenceAggregate(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	exitCode := 1
	row := serverstore.EvidenceRow{
		PURL: "pkg:golang/example.com/stagefixture@v1.0.0", Symbol: "Parse",
		EnvHash: "env", EnvJSON: string(domain.MustCanonicalJSON(domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "golang", OS: "linux", Arch: "amd64"})),
		Stage: "PROJECT_COMPILE", Result: "FAIL",
		ErrorFingerprint: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		TerminationKind:  string(domain.TerminationExit), ExitCode: &exitCode,
		EvidenceQuality: string(domain.EvidencePartial), ActualToolchain: "typescript/tsc",
		StageEvidence: string(domain.FailureStageCompilerDiagnostic),
		OuterCommand:  "go test", OuterCommands: []string{"go test", "npm test"},
		ObservationCount: 2, FirstSeen: now, LastSeen: now,
	}
	clusters := BuildClusters("golang", "example.com/stagefixture", map[string][]serverstore.EvidenceRow{"v1.0.0": {row}}, nil, now)
	if len(clusters) != 1 || strings.Join(clusters[0].OuterCommands, ",") != "go test,npm test" {
		t.Fatalf("cluster outer commands = %+v", clusters)
	}
}

func failureEvidenceRow(osName, fp string, term domain.TerminationKind, quality domain.EvidenceQuality, count int64, now time.Time) serverstore.EvidenceRow {
	env := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", OS: osName, Arch: "amd64", Runtime: "go", RuntimeVersion: "1.26"}.Normalize()
	return serverstore.EvidenceRow{
		PURL: "pkg:golang/github.com/jackc/pgx/v5@v5.10.0", Symbol: "ParseConfig",
		EnvHash: env.Hash(), EnvJSON: string(domain.MustCanonicalJSON(env)), Stage: "PROJECT_TEST", Result: "FAIL",
		ErrorFingerprint: fp, ErrorSummary: "connection refused <ip>:<port>", TerminationKind: string(term),
		EvidenceQuality: string(quality), ObservationCount: count, FirstSeen: now.Add(-time.Hour), LastSeen: now,
	}
}
