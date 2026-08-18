package compatibility

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const (
	jdkTestCase = "case:jdk-boundary"
	jdkTestPURL = "pkg:maven/com.example/library@1.2.3"
)

func jdkTestManifest(adapter, languageVersion string) domain.SampleManifest {
	return domain.SampleManifest{
		SchemaVersion: 1,
		Case:          domain.Case{SchemaVersion: 1, CaseID: jdkTestCase},
		Packages:      []string{jdkTestPURL}, Symbols: []string{"Library.call"},
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "maven", Runtime: "java",
			Language: "java", LanguageVersion: languageVersion,
		},
		BuildCommand:    []string{"gradle", "--offline", "classes"},
		ContractCommand: []string{"gradle", "--offline", "contract"},
		VerifierAdapter: adapter,
	}
}

func jdkTestReceipt(runtimeVersion, compile, contract string) ReceiptInfo {
	return ReceiptInfo{
		PeerID: "ed25519:test", CaseID: jdkTestCase,
		Env: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "maven", OS: "linux",
			OSVersionBucket: "2023", Distro: "amzn", Libc: "glibc", LibcVersion: "2.34",
			Arch: "x64", Runtime: "java", RuntimeVersion: runtimeVersion,
			Language: "java", LanguageVersion: "8", Compiler: "javac", CompilerVersion: runtimeVersion,
			PackageManager: "gradle", PackageManagerVersion: "8.14.3",
			ExecutionContext: "java", Virtualization: "container", ContainerRuntime: "docker",
		},
		Stages: map[string]string{
			"resolve": string(domain.ResultPass), "compile": compile, "contract": contract,
		},
		ContractResult:   contract,
		ResolvedPackages: []domain.PURL{{Ecosystem: "maven", Name: "com.example/library", Version: "1.2.3"}},
		VerifierAdapter:  "gradle-java@1", SandboxCapability: domain.CapContainerRun,
	}
}

func jdkTestSamples(receipts ...ReceiptInfo) []sampleData {
	return []sampleData{{
		row:      serverstore.SampleRow{CaseID: jdkTestCase},
		manifest: jdkTestManifest("gradle-java@1", "8"), receipts: receipts,
	}}
}

func TestJDKBoundaryFindsContractAndCompileBoundariesSeparately(t *testing.T) {
	contract := jdkBoundariesFromReceipts(jdkTestSamples(
		jdkTestReceipt("8", "PASS", "PASS"),
		jdkTestReceipt("11", "PASS", "FAIL"),
	))[receiptTarget{purl: jdkTestPURL, symbol: "Library.call"}]
	if len(contract) != 1 || contract[0].Stage != string(domain.StageContract) ||
		contract[0].LowerRuntimeVersion != "8" || contract[0].HigherRuntimeVersion != "11" ||
		contract[0].LowerResult != "PASS" || contract[0].HigherResult != "FAIL" ||
		contract[0].LanguageVersion != "8" || len(contract[0].ResolvedPackages) != 1 {
		t.Fatalf("contract JDK boundary = %+v", contract)
	}

	compile := jdkBoundariesFromReceipts(jdkTestSamples(
		jdkTestReceipt("8", "PASS", "PASS"),
		jdkTestReceipt("11", "FAIL", ""),
	))[receiptTarget{purl: jdkTestPURL, symbol: "Library.call"}]
	if len(compile) != 1 || compile[0].Stage != string(domain.StageCompile) ||
		compile[0].LowerResult != "PASS" || compile[0].HigherResult != "FAIL" {
		t.Fatalf("compile JDK boundary = %+v", compile)
	}

	recovery := jdkBoundariesFromReceipts(jdkTestSamples(
		jdkTestReceipt("8", "PASS", "FAIL"),
		jdkTestReceipt("11", "PASS", "PASS"),
	))[receiptTarget{purl: jdkTestPURL, symbol: "Library.call"}]
	if len(recovery) != 1 || recovery[0].Stage != string(domain.StageContract) ||
		recovery[0].LowerResult != "FAIL" || recovery[0].HigherResult != "PASS" {
		t.Fatalf("contract JDK recovery boundary = %+v", recovery)
	}
}

func TestJDKBoundarySuppressesMixedEndpointAndNonJDKDifferences(t *testing.T) {
	lower := jdkTestReceipt("8", "PASS", "PASS")
	higherFail := jdkTestReceipt("11", "PASS", "FAIL")
	higherPass := jdkTestReceipt("11", "PASS", "PASS")
	if got := jdkBoundariesFromReceipts(jdkTestSamples(lower, higherFail, higherPass)); len(got) != 0 {
		t.Fatalf("mixed endpoint produced a boundary: %+v", got)
	}

	tests := map[string]func(*ReceiptInfo){
		"os":                            func(r *ReceiptInfo) { r.Env.OS = "windows" },
		"distro":                        func(r *ReceiptInfo) { r.Env.Distro = "alpine" },
		"libc":                          func(r *ReceiptInfo) { r.Env.Libc = "musl" },
		"arch":                          func(r *ReceiptInfo) { r.Env.Arch = "arm64" },
		"package manager exact version": func(r *ReceiptInfo) { r.Env.PackageManagerVersion = "9.7.0" },
		"package manager name":          func(r *ReceiptInfo) { r.Env.PackageManager = "maven" },
		"container runtime":             func(r *ReceiptInfo) { r.Env.ContainerRuntime = "podman" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			higher := higherFail
			change(&higher)
			if got := jdkBoundariesFromReceipts(jdkTestSamples(lower, higher)); len(got) != 0 {
				t.Fatalf("non-JDK difference produced a boundary: %+v", got)
			}
		})
	}

	differentPackages := higherFail
	differentPackages.ResolvedPackages = []domain.PURL{{Ecosystem: "maven", Name: "com.example/library", Version: "2.0.0"}}
	if got := jdkBoundariesFromReceipts(jdkTestSamples(lower, differentPackages)); len(got) != 0 {
		t.Fatalf("different resolved package set produced a boundary: %+v", got)
	}
}

func TestJDKBoundaryNeverCombinesDifferentArtifacts(t *testing.T) {
	lower := sampleData{
		row:      serverstore.SampleRow{SampleID: "sha256:lower", CaseID: jdkTestCase},
		manifest: jdkTestManifest("gradle-java@1", "8"),
		receipts: []ReceiptInfo{jdkTestReceipt("8", "PASS", "PASS")},
	}
	higher := sampleData{
		row:      serverstore.SampleRow{SampleID: "sha256:higher", CaseID: jdkTestCase},
		manifest: jdkTestManifest("gradle-java@1", "8"),
		receipts: []ReceiptInfo{jdkTestReceipt("11", "PASS", "FAIL")},
	}
	if got := jdkBoundariesFromReceipts([]sampleData{lower, higher}); len(got) != 0 {
		t.Fatalf("different immutable artifacts produced a JDK boundary: %+v", got)
	}
}

func TestJDKBoundaryRequiresPinnedFamilyResolveAndExplicitTarget(t *testing.T) {
	lower := jdkTestReceipt("8", "PASS", "PASS")
	higher := jdkTestReceipt("11", "PASS", "FAIL")

	badResolve := higher
	badResolve.Stages = cloneStages(higher.Stages)
	badResolve.Stages["resolve"] = "FAIL"
	if got := jdkBoundariesFromReceipts(jdkTestSamples(lower, badResolve)); len(got) != 0 {
		t.Fatalf("failed resolve produced a boundary: %+v", got)
	}

	nonPinned := higher
	nonPinned.Env.Distro = "debian"
	if got := jdkBoundariesFromReceipts(jdkTestSamples(lower, nonPinned)); len(got) != 0 {
		t.Fatalf("non-pinned image family produced a boundary: %+v", got)
	}

	missingTarget := jdkTestSamples(lower, higher)
	missingTarget[0].manifest.Environment.LanguageVersion = ""
	if got := jdkBoundariesFromReceipts(missingTarget); len(got) != 0 {
		t.Fatalf("missing explicit language target produced a boundary: %+v", got)
	}

	// Gradle 25 uses a different exact Gradle release, so this is not a
	// one-variable JDK comparison against the Gradle 8.14.3 images.
	jdk25 := jdkTestReceipt("25", "PASS", "FAIL")
	jdk25.Env.PackageManagerVersion = "9.7.0"
	if got := jdkBoundariesFromReceipts(jdkTestSamples(lower, jdk25)); len(got) != 0 {
		t.Fatalf("Gradle-version change produced a JDK-only boundary: %+v", got)
	}
}

func TestMavenJDKBoundaryRequiresConservativelyPinnedLanguageCommand(t *testing.T) {
	lower := jdkTestReceipt("8", "PASS", "PASS")
	higher := jdkTestReceipt("11", "PASS", "FAIL")
	for _, receipt := range []*ReceiptInfo{&lower, &higher} {
		receipt.VerifierAdapter = "maven-java@1"
		receipt.Env.PackageManager = "maven"
		receipt.Env.PackageManagerVersion = "3.9.11"
	}
	manifest := jdkTestManifest("maven-java@1", "8")
	manifest.BuildCommand = []string{"javac", "-source", "8", "-target", "8", "src/Contract.java"}
	samples := []sampleData{{row: serverstore.SampleRow{CaseID: jdkTestCase}, manifest: manifest, receipts: []ReceiptInfo{lower, higher}}}
	if got := jdkBoundariesFromReceipts(samples); len(got) != 1 {
		t.Fatalf("direct pinned javac command did not produce boundary: %+v", got)
	}

	manifest.BuildCommand = []string{"sh", "-c", "javac --release 8 src/Contract.java"}
	samples[0].manifest = manifest
	if got := jdkBoundariesFromReceipts(samples); len(got) != 0 {
		t.Fatalf("shell Maven command was treated as a proven target: %+v", got)
	}

	for _, ambiguous := range [][]string{
		{"javac", "--release", "8", "--release", "11", "src/Contract.java"},
		{"javac", "--release", "8", "@compiler.args"},
		{"javac", "-source", "8", "-target", "11", "src/Contract.java"},
	} {
		manifest.BuildCommand = ambiguous
		samples[0].manifest = manifest
		if got := jdkBoundariesFromReceipts(samples); len(got) != 0 {
			t.Fatalf("ambiguous Maven command %q produced a boundary: %+v", ambiguous, got)
		}
	}
}

func TestBuilderPublishesJDKBoundaryApartFromPackageRegression(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	manifest := jdkTestManifest("gradle-java@1", "8")
	sampleID := "sha256:" + strings.Repeat("a", 64)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: sampleID, CaseID: jdkTestCase,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status:       "PUBLISHED", License: "MIT-0", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for i, info := range []ReceiptInfo{
		jdkTestReceipt("8", "PASS", "PASS"),
		jdkTestReceipt("11", "PASS", "FAIL"),
	} {
		receipt := domain.VerificationReceipt{
			SchemaVersion: 2, SampleID: sampleID, CaseID: jdkTestCase,
			EnvironmentHash: info.Env.Hash(), Environment: info.Env, Stages: info.Stages,
			ResolvedPackages: []string{jdkTestPURL}, VerifierAdapter: info.VerifierAdapter,
			SandboxCapability: info.SandboxCapability, PeerID: info.PeerID + string(rune('a'+i)),
			CreatedAt: now.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
		}
		if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
			ReceiptID: receipt.ReceiptID(), SampleID: sampleID, PeerID: receipt.PeerID,
			ReceiptJSON: string(domain.MustCanonicalJSON(receipt)), ContractResult: info.ContractResult,
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&Builder{Store: store, Now: func() time.Time { return now }}).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	raw, ok, err := store.GetSnapshot(ctx, jdkTestPURL, "Library.call")
	if err != nil || !ok {
		t.Fatalf("snapshot: ok=%v err=%v", ok, err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.JDKBoundaryCandidates) != 1 || len(snapshot.RegressionCandidates) != 0 {
		t.Fatalf("published candidates = JDK:%+v package:%+v", snapshot.JDKBoundaryCandidates, snapshot.RegressionCandidates)
	}
}

func cloneStages(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
