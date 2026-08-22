package verifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// fakeRunner returns scripted stage results and records the call order.
type fakeRunner struct {
	resolve      sandbox.StageResult
	build        sandbox.StageResult
	contract     sandbox.StageResult
	calls        []string
	resolveHook  func(string)
	buildHook    func(string)
	contractHook func(string)
	noImage      bool
}

func (f *fakeRunner) Resolve(ctx context.Context, dir string, m domain.SampleManifest) sandbox.StageResult {
	f.calls = append(f.calls, "resolve")
	if f.resolveHook != nil {
		f.resolveHook(dir)
	}
	return f.resolve
}
func (f *fakeRunner) Build(ctx context.Context, dir string, m domain.SampleManifest) sandbox.StageResult {
	f.calls = append(f.calls, "build")
	if f.buildHook != nil {
		f.buildHook(dir)
	}
	return f.build
}
func (f *fakeRunner) Contract(ctx context.Context, dir string, m domain.SampleManifest) sandbox.StageResult {
	f.calls = append(f.calls, "contract")
	if f.contractHook != nil {
		f.contractHook(dir)
	}
	return f.contract
}

// StageEnvironment mirrors the container runner: stages run somewhere else
// than the host, and the receipt must say so.
func (f *fakeRunner) StageEnvironment(host domain.EnvironmentFingerprint, m domain.SampleManifest) domain.EnvironmentFingerprint {
	env := host
	env.OS = "linux"
	env.ExecutionContext = "node"
	return env.Normalize()
}

// VerifierImage mirrors the container runner: the receipt names the image
// bytes the stages ran in, and the signature covers them.
func (f *fakeRunner) VerifierImage(domain.SampleManifest) *domain.VerifierImage {
	if f.noImage {
		return nil
	}
	return &domain.VerifierImage{
		Reference: "node:22-alpine@sha256:" + strings.Repeat("a", 64),
		Digest:    "sha256:" + strings.Repeat("a", 64),
	}
}

func allPassRunner() *fakeRunner {
	return &fakeRunner{
		resolve:  sandbox.StageResult{Result: sandbox.ResultPass, Log: "resolved 0 packages"},
		build:    sandbox.StageResult{Result: sandbox.ResultPass, Log: "compiled"},
		contract: sandbox.StageResult{Result: sandbox.ResultPass, Log: "contract ok"},
	}
}

func testEnv() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18.0",
	}
}

func testManifest() domain.SampleManifest {
	return domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW", Goal: "Echo a string",
			Packages: []string{"pkg:npm/axios@1.12.0"},
			Contract: []string{"echo returns its input"},
		},
		Packages:        []string{"pkg:npm/axios@1.12.0"},
		Environment:     testEnv(),
		License:         "MIT-0",
		ContractCommand: []string{"node", "test/contract.mjs"},
		VerifierAdapter: "node-typescript@1",
	}
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("csx.json", `{"schemaVersion":1}`)
	write("src/echo.mjs", "export function echo(x){ return x }\n")
	return dir
}

func writeFixtureFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEngineAllPassReceipt(t *testing.T) {
	dir := fixtureDir(t)
	r := allPassRunner()
	ident := testIdentity(t)
	env := testEnv()

	receipt, err := Run(context.Background(), r, domain.CapContainerRun, dir, testManifest(), ident, env)
	if err != nil {
		t.Fatal(err)
	}

	_, wantID, err := samples.BuildArtifact(dir)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SampleID != wantID {
		t.Fatalf("sample id %s, want %s", receipt.SampleID, wantID)
	}
	if receipt.SchemaVersion != 2 {
		t.Fatalf("schemaVersion %d", receipt.SchemaVersion)
	}
	if receipt.CaseID == "" {
		t.Fatal("case id empty")
	}
	// The receipt names where the stages ran (the runner's environment),
	// not the host that launched them — self-consistently hashed.
	wantEnv := r.StageEnvironment(env, testManifest())
	if receipt.EnvironmentHash != wantEnv.Hash() || receipt.Environment.Hash() != wantEnv.Hash() {
		t.Fatalf("receipt environment = %+v, want the runner's stage environment %+v",
			receipt.Environment, wantEnv)
	}
	if receipt.Environment.OS == env.OS && env.OS != wantEnv.OS {
		t.Error("receipt recorded the host OS for a containerised run")
	}
	if receipt.SandboxCapability != domain.CapContainerRun {
		t.Fatalf("capability %s", receipt.SandboxCapability)
	}
	for stage, want := range map[string]string{
		"resolve": "PASS", "compile": "PASS", "contract": "PASS", "load": "PASS",
	} {
		if receipt.Stages[stage] != want {
			t.Errorf("stage %s = %s, want %s", stage, receipt.Stages[stage], want)
		}
	}
	// logsDigest covers the concatenated stage logs; logs stay local.
	wantDigest := domain.SHA256Hex([]byte("resolved 0 packages\ncompiled\ncontract ok"))
	if receipt.LogsDigest != wantDigest {
		t.Fatalf("logs digest %s, want %s", receipt.LogsDigest, wantDigest)
	}
	created, err := time.Parse(time.RFC3339, receipt.CreatedAt)
	if err != nil {
		t.Fatalf("createdAt not RFC3339: %v", err)
	}
	if created.Location() != time.UTC && created.UTC().Format(time.RFC3339) != receipt.CreatedAt {
		t.Fatalf("createdAt not UTC: %s", receipt.CreatedAt)
	}
	if receipt.PeerID != ident.PeerID() || receipt.PeerPubkey != ident.PubkeyB64() {
		t.Fatal("peer identity fields wrong")
	}
}

func TestEngineSignatureVerifiesAndTamperBreaks(t *testing.T) {
	dir := fixtureDir(t)
	writeFixtureFile(t, dir, "package-lock.json", `{"packages":{"node_modules/axios":{"version":"1.12.4","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.4.tgz"}}}`)
	writeFixtureFile(t, dir, "node_modules/axios/package.json", `{"name":"axios","version":"1.12.4"}`)
	ident := testIdentity(t)

	receipt, err := Run(context.Background(), allPassRunner(), domain.CapContainerRun, dir, testManifest(), ident, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !identity.Verify(receipt.PeerPubkey, receipt.PeerSignature, receipt.SigningBytes()) {
		t.Fatal("signature must verify")
	}

	tampered := receipt
	tampered.Stages = map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS", "load": "PASS"}
	tampered.Stages["contract"] = "FAIL"
	if identity.Verify(tampered.PeerPubkey, tampered.PeerSignature, tampered.SigningBytes()) {
		t.Fatal("tampered stages must break the signature")
	}

	tampered2 := receipt
	tampered2.SampleID = "sha256:" + "00" + receipt.SampleID[9:]
	if identity.Verify(tampered2.PeerPubkey, tampered2.PeerSignature, tampered2.SigningBytes()) {
		t.Fatal("tampered sample id must break the signature")
	}

	tampered3 := receipt
	tampered3.ResolvedPackages = []string{"pkg:npm/axios@1.13.0"}
	if identity.Verify(tampered3.PeerPubkey, tampered3.PeerSignature, tampered3.SigningBytes()) {
		t.Fatal("tampered resolved packages must break the signature")
	}
}

func TestEngineSnapshotsResolvedPackagesBeforeSampleCodeRuns(t *testing.T) {
	dir := fixtureDir(t)
	writeFixtureFile(t, dir, "package-lock.json", `{"packages":{"node_modules/axios":{"version":"1.12.4","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.4.tgz"}}}`)
	writeFixtureFile(t, dir, "node_modules/axios/package.json", `{"name":"axios","version":"1.12.4"}`)
	r := allPassRunner()
	r.resolveHook = func(dir string) {
		writeFixtureFile(t, dir, "package-lock.json", `{"packages":{"node_modules/axios":{"version":"1.12.4","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.4.tgz"}}}`)
		writeFixtureFile(t, dir, "node_modules/axios/package.json", `{"name":"axios","version":"1.12.4"}`)
	}
	r.contractHook = func(dir string) {
		writeFixtureFile(t, dir, "package-lock.json", `{"packages":{"node_modules/axios":{"version":"9.9.9","resolved":"https://registry.npmjs.org/axios/-/axios-9.9.9.tgz"}}}`)
		writeFixtureFile(t, dir, "node_modules/axios/package.json", `{"name":"axios","version":"9.9.9"}`)
	}

	receipt, err := Run(context.Background(), r, domain.CapContainerRun, dir, testManifest(), testIdentity(t), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pkg:npm/axios@1.12.4"}
	if len(receipt.ResolvedPackages) != 1 || receipt.ResolvedPackages[0] != want[0] {
		t.Fatalf("resolved packages = %v, want resolve-stage snapshot %v", receipt.ResolvedPackages, want)
	}
}

func TestEngineRejectsResolveMutationOfImmutableSampleContent(t *testing.T) {
	dir := fixtureDir(t)
	r := allPassRunner()
	r.resolveHook = func(dir string) {
		writeFixtureFile(t, dir, "src/echo.mjs", "export function echo(){ return 'forged' }\n")
	}

	receipt, err := Run(context.Background(), r, domain.CapContainerRun, dir, testManifest(), testIdentity(t), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Stages["resolve"] != sandbox.ResultFail {
		t.Fatalf("resolve = %q, want FAIL after source mutation", receipt.Stages["resolve"])
	}
	for _, stage := range []string{"compile", "contract", "load"} {
		if receipt.Stages[stage] != sandbox.ResultSkipped {
			t.Errorf("%s = %q, want SKIPPED", stage, receipt.Stages[stage])
		}
	}
	if len(r.calls) != 1 || r.calls[0] != "resolve" {
		t.Fatalf("later stages ran after integrity failure: %v", r.calls)
	}
	if len(receipt.ResolvedPackages) != 0 {
		t.Fatalf("mutating resolve produced package claims: %v", receipt.ResolvedPackages)
	}
}

func TestEngineResolveFailShortCircuits(t *testing.T) {
	r := allPassRunner()
	r.resolve = sandbox.StageResult{Result: sandbox.ResultFail, Log: "npm ERR"}
	dir := fixtureDir(t)
	writeFixtureFile(t, dir, "package-lock.json", `{"packages":{"node_modules/axios":{"version":"1.12.4"}}}`)

	receipt, err := Run(context.Background(), r, domain.CapContainerRun, dir, testManifest(), testIdentity(t), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Stages["resolve"] != "FAIL" {
		t.Fatalf("resolve %s", receipt.Stages["resolve"])
	}
	for _, stage := range []string{"compile", "contract", "load"} {
		if receipt.Stages[stage] != "SKIPPED" {
			t.Errorf("stage %s = %s, want SKIPPED", stage, receipt.Stages[stage])
		}
	}
	if len(r.calls) != 1 || r.calls[0] != "resolve" {
		t.Fatalf("later stages must not run after FAIL: %v", r.calls)
	}
	if len(receipt.ResolvedPackages) != 0 {
		t.Fatalf("failed resolve claimed pre-existing lockfile versions: %v", receipt.ResolvedPackages)
	}
}

func TestEngineSkippedResolveDoesNotClaimPreexistingLockfile(t *testing.T) {
	r := allPassRunner()
	r.resolve = sandbox.StageResult{Result: sandbox.ResultSkipped, Log: "no isolation"}
	dir := fixtureDir(t)
	writeFixtureFile(t, dir, "package-lock.json", `{"packages":{"node_modules/axios":{"version":"1.12.4"}}}`)

	receipt, err := Run(context.Background(), r, domain.CapCompileOnly, dir, testManifest(), testIdentity(t), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.ResolvedPackages) != 0 {
		t.Fatalf("skipped resolve claimed pre-existing lockfile versions: %v", receipt.ResolvedPackages)
	}
}

func TestEngineCompileFailSkipsContract(t *testing.T) {
	r := allPassRunner()
	r.build = sandbox.StageResult{Result: sandbox.ResultFail, Log: "tsc error"}

	receipt, err := Run(context.Background(), r, domain.CapContainerRun, fixtureDir(t), testManifest(), testIdentity(t), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Stages["compile"] != "FAIL" || receipt.Stages["contract"] != "SKIPPED" || receipt.Stages["load"] != "SKIPPED" {
		t.Fatalf("stages: %v", receipt.Stages)
	}
	if len(r.calls) != 2 {
		t.Fatalf("contract must not run after compile FAIL: %v", r.calls)
	}
}

func TestEngineCompileOnlySkipsContractHonestly(t *testing.T) {
	// A COMPILE_ONLY runner (native) reports contract SKIPPED; load must
	// then also be SKIPPED — never inferred as PASS (goal.md §3.5).
	r := allPassRunner()
	r.contract = sandbox.StageResult{Result: sandbox.ResultSkipped, Log: "no isolation"}

	receipt, err := Run(context.Background(), r, domain.CapCompileOnly, fixtureDir(t), testManifest(), testIdentity(t), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Stages["contract"] != "SKIPPED" || receipt.Stages["load"] != "SKIPPED" {
		t.Fatalf("stages: %v", receipt.Stages)
	}
	if receipt.SandboxCapability != domain.CapCompileOnly {
		t.Fatalf("capability %s", receipt.SandboxCapability)
	}
}
