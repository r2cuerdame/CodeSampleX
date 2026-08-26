package compatibility

import (
	"encoding/json"
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

func failureEvidenceRow(osName, fp string, term domain.TerminationKind, quality domain.EvidenceQuality, count int64, now time.Time) serverstore.EvidenceRow {
	env := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", OS: osName, Arch: "amd64", Runtime: "go", RuntimeVersion: "1.26"}.Normalize()
	return serverstore.EvidenceRow{
		PURL: "pkg:golang/github.com/jackc/pgx/v5@v5.10.0", Symbol: "ParseConfig",
		EnvHash: env.Hash(), EnvJSON: string(domain.MustCanonicalJSON(env)), Stage: "PROJECT_TEST", Result: "FAIL",
		ErrorFingerprint: fp, ErrorSummary: "connection refused <ip>:<port>", TerminationKind: string(term),
		EvidenceQuality: string(quality), ObservationCount: count, FirstSeen: now.Add(-time.Hour), LastSeen: now,
	}
}
