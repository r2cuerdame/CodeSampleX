package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/measurement"
)

// TestSchemaFixtures validates a fully populated fixture of every Go type
// against its published schema, in schemas/v1 and schemas/v2, at every depth.
// Optional fields are populated on purpose: an omitempty field left at its
// zero value never reaches the document, which is exactly how #324, #326,
// #327 and #337 each passed this test while the schema rejected production
// documents (#345).
func TestSchemaFixtures(t *testing.T) {
	root := filepath.Dir(schemaDir(t))
	hex64 := strings.Repeat("ab", 32)
	exit := 1
	env := func(eco string) EnvironmentFingerprint {
		return EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: eco, OS: "linux", Arch: "x64",
			Runtime: "node", RuntimeVersion: "22", Compiler: "tsc", CompilerVersion: "5.9",
			PackageManager: "npm", PackageManagerVersion: "10", Libc: "musl", Distro: "alpine"}
	}
	caseFixture := Case{SchemaVersion: 1, CaseID: "case:sha256:" + hex64, Kind: "HOW", Goal: "g",
		Packages: []string{"pkg:npm/axios@1.12.0"}, Symbols: []string{"axios.get"},
		Constraints: map[string]string{"runtime": "node"}, Contract: []string{"c"},
		Believed: "axios.get resolves on a 404"}
	manifest := SampleManifest{SchemaVersion: 1, Case: caseFixture,
		Packages: []string{"pkg:composer/symfony/console@7.4.0"}, Symbols: []string{"Application"},
		Subject:     "pkg:composer/symfony/console@7.4.0",
		Environment: env("composer"), License: "MIT-0", BuildCommand: []string{"composer", "install"},
		ContractCommand: []string{"php", "test/contract.php"}, VerifierAdapter: "composer@1"}
	receipt := func(version int) VerificationReceipt {
		return VerificationReceipt{SchemaVersion: version, SampleID: "sha256:" + hex64, CaseID: "case:x",
			EnvironmentHash: "sha256:" + hex64, Environment: env("npm"),
			Stages: map[string]string{"resolve": "PASS"}, VerifierAdapter: "node-typescript@1",
			SandboxCapability: CapContainerRun, LogsDigest: "sha256:" + hex64, CreatedAt: "2026-08-13T00:00:00Z",
			PeerID: "ed25519:0123456789abcdef", PeerPubkey: "pk", PeerSignature: "sig"}
	}
	receiptV2 := receipt(2)
	receiptV2.Stages = map[string]string{"resolve": "PASS", "contract": "FAIL"}
	receiptV2.StageFailures = map[string]FailureEvidence{"contract": {
		TerminationKind: TerminationExit, ExitCode: &exit, ErrorSummary: "boom", ErrorCode: "E1",
		Fingerprint: "sha256:" + hex64, EvidenceQuality: EvidenceComplete, OuterCommand: "npm test",
		OuterStage: StageProjectTest, ActualToolchain: "node/test-runner",
		StageEvidence: FailureStageCompilerDiagnostic, EvidenceGap: FailureDiagnosticMissing}}
	receiptV2.ResolvedPackages = []string{"pkg:npm/axios@1.12.0", "pkg:composer/symfony/console@7.4.0"}
	receiptV2.VerifierImage = &VerifierImage{Reference: "node:22-alpine@sha256:" + hex64, Digest: "sha256:" + hex64}
	batch := func(version int, purl, eco string) ObservationBatch {
		return ObservationBatch{SchemaVersion: version, Epoch: "2026-08-13",
			AnonID: "0123456789abcdef", ProjectBucket: "0123456789ab", Package: purl,
			Symbol: "s", SymbolConfidence: SymbolProbable, Environment: env(eco),
			Stage: StageProjectCompile, Result: ResultFail, ObservationCount: 3,
			ErrorFingerprint: "sha256:" + hex64, ErrorCode: "E1", TerminationKind: TerminationExit,
			ExitCode: &exit, ErrorSummary: "boom", EvidenceQuality: EvidenceComplete}
	}
	batchV2 := func(purl, eco string) ObservationBatch {
		b := batch(2, purl, eco)
		b.OuterCommand, b.OuterStage, b.ActualToolchain = "go test", StageProjectTest, "go/compiler"
		b.StageEvidence, b.FailureEvidenceGap = FailureStageCompilerDiagnostic, FailureDiagnosticMissing
		b.Direct, b.Coresident = true, []string{"1.1.0"}
		b.DependsOn = []string{strings.Replace(purl, "@", "-dep@", 1)}
		return b
	}
	leaf := batchV2("pkg:hex/jason@1.4.4", "hex")
	leaf.DependsOn, leaf.DependsOnNone = nil, true
	result := func(version int) SearchResult {
		r := SearchResult{Grade: GradeCompatible, Confidence: "LOW", Score: 0.5, Case: &caseFixture,
			SampleID: "sha256:" + hex64, SampleStatus: "published",
			Exact: []string{"os"}, Different: []string{"arch"}, Adaptation: []string{},
			Evidence: EvidenceSummary{Confidence: "LOW"}, KnownFailures: []KnownFailure{{Count: 1}}}
		if version >= 2 {
			r.SampleURL, r.ExactFailureMatched = "https://codesamplex.dev/s/x", true
		}
		return r
	}
	searchRequest := func(version int) SearchRequest {
		r := SearchRequest{SchemaVersion: version, Query: "q", Packages: []string{"pkg:npm/axios@1.12.0"},
			Symbols: []string{"axios.get"}, Environment: env("npm"), ErrorFingerprint: "sha256:" + hex64,
			ErrorCode: "E1", Limit: 3}
		if version >= 2 {
			r.ProjectPackages, r.ContextSymbols = []string{"pkg:npm/react@19.0.0"}, []string{"useState"}
			r.SymbolProvenance, r.EnvironmentProvenance = SearchProvenanceExplicit, SearchProvenanceContext
			r.ErrorFingerprints, r.Debug = []string{"sha256:" + hex64}, true
		}
		return r
	}
	cliEnv := EnvironmentFingerprint{SchemaVersion: 1, OS: "windows", Arch: "x64"}
	cliObservation := CLIExperienceObservation{ID: "x",
		Coordinate: CLIExperienceCoordinate{Tool: "git", ToolVersion: "2.55.0", Subcommand: "status",
			ArgsPattern: "--short", Shell: "direct", Environment: cliEnv},
		Provenance: ProvenanceField, Result: ResultFail, Stage: StageProjectProcess,
		Termination:      FailureTermination{Kind: TerminationExit, ExitCode: &exit},
		ErrorFingerprint: "sha256:" + hex64, ErrorCode: "E1", ErrorSummary: "boom",
		EvidenceQuality: EvidenceComplete, OuterStage: StageProjectProcess, ActualToolchain: "git",
		StageEvidence: FailureStageStructuredTermination, FailureEvidenceGap: FailureDiagnosticMissing,
		ObservedAt: "2026-08-13T00:00:01Z", StartedAt: "2026-08-13T00:00:00Z", FinishedAt: "2026-08-13T00:00:01Z",
		// The canonical identity, as the recorder and localdb both stamp it (#350).
		EnvironmentID: cliEnv.Hash(),
		Stdout:        CLIStreamEvidence{Fingerprint: "sha256:" + hex64, Excerpt: "out", Truncated: true},
		Stderr:        CLIStreamEvidence{Fingerprint: "sha256:" + hex64},
		Count:         1, IsHighInformation: true}
	shard := func(key string) json.RawMessage {
		return json.RawMessage(`{"schemaVersion":1,"key":"` + key + `","generatedAt":"2026-08-13T00:00:00Z",` +
			`"packages":[{"purl":"pkg:x/y@1","canonicalCaseCountTotal":1,"distinctSubjectCountTotal":1,` +
			`"symbols":[{"family":"f","stats":{}}],"samples":[{"symbols":["a"],"symbolsTruncated":false}]}]}`)
	}
	var purls []any
	for _, p := range []string{"pkg:npm/%40scope/name@1.0.0", "pkg:pypi/requests@2.32.3", "pkg:cargo/serde@1.0.0",
		"pkg:golang/github.com/pkg/errors@v0.9.1", "pkg:maven/com.google.guava/guava@33.0.0-jre",
		"pkg:composer/symfony/console@7.4.0", "pkg:gem/rack@3.1.8", "pkg:hex/jason@1.4.4",
		"pkg:pub/yaml@3.1.2", "pkg:generic/cli/gh@2.78.0"} {
		purls = append(purls, p)
	}

	fixtures := map[string][]any{
		"v1/environment.json":            {env("npm"), env("generic")},
		"v1/observation-batch.json":      {batch(1, "pkg:npm/axios@1.12.0", "npm")},
		"v1/case.json":                   {caseFixture},
		"v1/sample-manifest.json":        {manifest},
		"v1/verification-receipt.json":   {receipt(1)},
		"v1/search-request.json":         {searchRequest(1)},
		"v1/cli-execution-evidence.json": {cliObservation},
		"v1/search-response.json": {SearchResponse{SchemaVersion: 1, Results: []SearchResult{}, Miss: true},
			SearchResponse{SchemaVersion: 1, Results: []SearchResult{result(1)}}},
		"v1/measurement-report.json": {measurement.NewTwoLayerReport("community",
			measurement.RetrievalQuality{}, measurement.OutcomeValue{})},
		"v1/shard.json": {shard("npm/axios/1"), shard("composer/symfony/console/7"), shard("gem/rails/7"),
			shard("hex/phoenix/1"), shard("pub/http/1")},
		"v1/purl.json": purls,
		"v2/observation-batch.json": {batchV2("pkg:golang/example.com/tool@v1.0.0", "golang"),
			batchV2("pkg:composer/symfony/console@7.4.0", "composer"), batchV2("pkg:gem/rack@3.1.8", "gem"),
			batchV2("pkg:pub/yaml@3.1.2", "pub"), leaf, batch(2, "pkg:generic/cli/gh@2.78.0", "generic")},
		"v2/search-request.json": {searchRequest(2)},
		"v2/search-response.json": {SearchResponse{SchemaVersion: 2, Results: []SearchResult{result(2)},
			Grade: GradeCompatible, Observed: &ObservedReports{}, CLIExperience: &CLIExperienceSummary{},
			Diagnostic: &DiagnosticTrace{}}},
		"v2/verification-receipt.json": {receiptV2},
	}

	v := newSchemaValidator(t)
	seen := 0
	for _, version := range []string{"v1", "v2"} {
		entries, err := os.ReadDir(filepath.Join(root, version))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := version + "/" + e.Name()
			schemaPath := filepath.Join(root, version, e.Name())
			v.load(schemaPath)
			seen++
			for i, fixture := range fixtures[name] {
				for _, msg := range v.validateGo(schemaPath, fixture) {
					t.Errorf("%s fixture %d: %s", name, i, msg)
				}
			}
		}
	}
	if seen < 18 {
		t.Fatalf("expected >=18 schema files across v1 and v2, found %d", seen)
	}
	for name := range fixtures {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Errorf("fixture for %s names no schema file", name)
		}
	}
}

// The validator has to reject what the schemas reject, or every pass above
// means nothing. Each case is one of the defects consolidated under #479.
func TestSchemaValidatorRejects(t *testing.T) {
	root := filepath.Dir(schemaDir(t))
	v := newSchemaValidator(t)
	hex64 := strings.Repeat("ab", 32)
	const obs = `"epoch":"2026-08-13","anonId":"0123456789abcdef","projectBucket":"0123456789ab","package":"pkg:npm/a@1",` +
		`"stage":"USED","result":"PASS","observationCount":1`
	const env = `{"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"x64"}`
	const caseDoc = `"schemaVersion":1,"kind":"HOW","goal":"g","packages":["p"],"contract":["c"]`
	const cliDoc = `"provenance":"field","result":"PASS","termination":{"kind":"exit"},"evidenceQuality":"complete","count":1,"isHighInformation":false`
	cases := []struct {
		schema string
		doc    any
	}{
		{"v1/purl.json", "npm/axios@1.12.0"},
		{"v1/purl.json", "pkg:npm/axios"},
		{"v1/shard.json", json.RawMessage(`{"schemaVersion":1,"key":"generic/x/1","generatedAt":"t","packages":[]}`)},
		{"v1/case.json", json.RawMessage(`{` + caseDoc + `,"unknown":1}`)},
		{"v1/case.json", json.RawMessage(`{` + caseDoc + `,"believed":"` + strings.Repeat("a", 601) + `"}`)},
		// A bare hex environmentId is what the schema used to demand (#350).
		{"v1/cli-execution-evidence.json", json.RawMessage(`{"coordinate":{"tool":"git","toolVersion":"1","shell":"direct",` +
			`"environment":{"schemaVersion":1,"os":"linux","arch":"x64"}},` + cliDoc +
			`,"startedAt":"t","finishedAt":"t","environmentId":"` + hex64 + `"}`)},
		// complete evidence without its required fields, through allOf/if/then.
		{"v1/cli-execution-evidence.json", json.RawMessage(`{"coordinate":{"tool":"git","environment":{}},` + cliDoc + `}`)},
		{"v2/observation-batch.json", json.RawMessage(`{"schemaVersion":2,` + obs + `,"environment":` + env + `,"dependsOn":["npm/b@1"]}`)},
		// Nested additionalProperties:false through a cross-directory $ref.
		{"v2/observation-batch.json", json.RawMessage(`{"schemaVersion":2,` + obs +
			`,"environment":{"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"x64","extra":"x"}}`)},
		// The frozen v1 batch has no dependency axis (#360).
		{"v1/observation-batch.json", json.RawMessage(`{"schemaVersion":1,` + obs + `,"environment":` + env + `,"direct":true}`)},
		{"v1/search-response.json", json.RawMessage(`{"schemaVersion":1,"results":[],"miss":true,"observed":{}}`)},
		{"v2/search-response.json", json.RawMessage(`{"schemaVersion":2,"miss":false,"results":[{"match":"EXACT","confidence":"HIGH",` +
			`"score":1,"exactFailureMatched":false,"exact":[],"different":[],"adaptationNeeded":[],"evidence":{},` +
			`"case":{` + caseDoc + `,"x":1}}]}`)},
	}
	for _, c := range cases {
		if errs := v.validateGo(filepath.Join(root, filepath.FromSlash(c.schema)), c.doc); len(errs) == 0 {
			t.Errorf("%s accepted a document it must reject: %s", c.schema, c.doc)
		}
	}
}
