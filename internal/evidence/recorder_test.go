package evidence

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func testDB(t *testing.T) *localdb.DB {
	t.Helper()
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatalf("open localdb: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	return id
}

func testEnvFP() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion:  1,
		Ecosystem:      "npm",
		OS:             "windows",
		Arch:           "x64",
		Runtime:        "node",
		RuntimeVersion: "22.18",
		ModuleSystem:   "cjs",
	}.Normalize()
}

// fakeScanResult mixes one PUBLIC package with a PRIVATE and an UNKNOWN
// one, plus a symbol usage on each of public and private.
func fakeScanResult() *scanner.ScanResult {
	axios := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	priv := domain.PURL{Ecosystem: "npm", Name: "corp-secret-lib", Version: "2.0.0"}
	unk := domain.PURL{Ecosystem: "npm", Name: "maybe-internal", Version: "0.3.1"}
	return &scanner.ScanResult{
		Packages: []scanner.ResolvedPackage{
			{PURL: axios, Publicness: scanner.PublicnessPublic, Direct: true, Source: "package-lock.json"},
			{PURL: priv, Publicness: scanner.PublicnessPrivate, Direct: true, Source: "package-lock.json"},
			{PURL: unk, Publicness: scanner.PublicnessUnknown, Direct: true, Source: "package-lock.json"},
		},
		Symbols: []scanner.SymbolUsage{
			{Package: axios, Family: "axios.post", Kind: "method", Confidence: domain.SymbolProbable},
			{Package: priv, Family: "corp-secret-lib.launch", Kind: "method", Confidence: domain.SymbolProbable},
		},
		Env: testEnvFP(),
	}
}

func knownProfile() scanner.CommandProfile {
	return scanner.CommandProfile{Stage: domain.StageProjectProcess, Known: true, Tool: "node"}
}

func pendingRows(t *testing.T, db *localdb.DB) []localdb.ObsRow {
	t.Helper()
	rows, err := db.PendingObservations(context.Background(), 100)
	if err != nil {
		t.Fatalf("pending observations: %v", err)
	}
	return rows
}

func TestRecordRunKnownPassRecordsPublicOnly(t *testing.T) {
	db := testDB(t)
	rec := &Recorder{DB: db, Ident: testIdentity(t), Cfg: config.Default()}
	dir := t.TempDir()

	if err := rec.RecordRun(context.Background(), dir, fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	rows := pendingRows(t, db)
	if len(rows) != 2 {
		t.Fatalf("want 2 observation rows (package + symbol), got %d: %+v", len(rows), rows)
	}
	epoch := time.Now().UTC().Format("2006-01-02")
	var sawPackage, sawSymbol bool
	for _, r := range rows {
		if r.PURL != "pkg:npm/axios@1.12.0" {
			t.Errorf("non-public purl in observations: %q", r.PURL)
		}
		if r.Epoch != epoch {
			t.Errorf("epoch = %q, want %q", r.Epoch, epoch)
		}
		if r.Stage != domain.StageProjectProcess || r.Result != domain.ResultPass {
			t.Errorf("stage/result = %s/%s, want PROJECT_PROCESS/PASS", r.Stage, r.Result)
		}
		if r.ErrorFP != "" || r.ErrorCode != "" {
			t.Errorf("PASS row carries error info: %+v", r)
		}
		switch r.Symbol {
		case "":
			sawPackage = true
		case "axios.post":
			sawSymbol = true
			if r.SymbolConfidence != domain.SymbolProbable {
				t.Errorf("symbol confidence = %q, want PROBABLE", r.SymbolConfidence)
			}
		default:
			t.Errorf("unexpected symbol row %q", r.Symbol)
		}
	}
	if !sawPackage || !sawSymbol {
		t.Fatalf("missing package or symbol row: package=%v symbol=%v", sawPackage, sawSymbol)
	}
}

func TestRecordRunNeverObservesPrivateOrUnknown(t *testing.T) {
	db := testDB(t)
	rec := &Recorder{DB: db, Ident: testIdentity(t), Cfg: config.Default()}

	// Exercise both the known and the unknown command paths.
	if err := rec.RecordRun(context.Background(), t.TempDir(), fakeScanResult(), knownProfile(), 1, "boom"); err != nil {
		t.Fatalf("RecordRun known: %v", err)
	}
	if err := rec.RecordRun(context.Background(), t.TempDir(), fakeScanResult(), scanner.CommandProfile{}, 0, ""); err != nil {
		t.Fatalf("RecordRun unknown: %v", err)
	}

	for _, r := range pendingRows(t, db) {
		if strings.Contains(r.PURL, "corp-secret-lib") || strings.Contains(r.PURL, "maybe-internal") {
			t.Errorf("PRIVATE/UNKNOWN package leaked into observations: %+v", r)
		}
		if strings.Contains(r.Symbol, "corp-secret-lib") {
			t.Errorf("private symbol leaked into observations: %+v", r)
		}
	}
}

func TestRecordRunFailAttachesSanitizedFingerprint(t *testing.T) {
	db := testDB(t)
	rec := &Recorder{DB: db, Ident: testIdentity(t), Cfg: config.Default()}

	stderrTail := `C:\Users\someone\proj\src\index.ts(10,5): error TS2345: Argument of type 'string' is not assignable.`
	profile := scanner.CommandProfile{Stage: domain.StageProjectTypecheck, Known: true, Tool: "tsc"}
	if err := rec.RecordRun(context.Background(), t.TempDir(), fakeScanResult(), profile, 2, stderrTail); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	rows := pendingRows(t, db)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Result != domain.ResultFail {
			t.Errorf("result = %s, want FAIL", r.Result)
		}
		if r.Stage != domain.StageProjectTypecheck {
			t.Errorf("stage = %s, want PROJECT_TYPECHECK", r.Stage)
		}
		if !strings.HasPrefix(r.ErrorFP, "sha256:") {
			t.Errorf("error fingerprint = %q, want sha256:<hex>", r.ErrorFP)
		}
		if r.ErrorCode != "TS2345" {
			t.Errorf("error code = %q, want TS2345", r.ErrorCode)
		}
		if strings.Contains(r.ErrorFP, `\`) || strings.Contains(r.ErrorCode, `\`) {
			t.Errorf("path fragment survived into row: %+v", r)
		}
	}
}

func TestRecordCommandOutputSplitsActualFailureEventsAndPersistsLineage(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	rec := &Recorder{DB: db, Ident: ident, Cfg: config.Default()}
	exitCode := 1
	profile := scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "go"}
	output := CommandOutput{
		Stdout:      "src/index.ts(12,5): error TS2352: bad conversion\n--- FAIL: TestMCP (0.01s)\n    mcp_test.go:20: got false, want true\nFAIL\n",
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode},
	}
	if err := rec.RecordCommandOutput(context.Background(), t.TempDir(), fakeScanResult(), profile,
		[]string{"go", "test", "./internal/mcp"}, exitCode, output); err != nil {
		t.Fatalf("RecordCommandOutput: %v", err)
	}

	rows := pendingRows(t, db)
	if len(rows) != 4 {
		t.Fatalf("want package+symbol for two failure events, got %d: %+v", len(rows), rows)
	}
	stages, toolchains, fingerprints := map[domain.Stage]bool{}, map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		stages[row.Stage] = true
		toolchains[row.ActualToolchain] = true
		fingerprints[row.ErrorFP] = true
		if row.OuterCommand != "go test" || row.OuterStage != domain.StageProjectTest {
			t.Errorf("outer lineage = %q / %q", row.OuterCommand, row.OuterStage)
		}
		if row.StageEvidence == "" || row.ErrorFP == "" {
			t.Errorf("untraceable classified failure: %+v", row)
		}
	}
	if !stages[domain.StageProjectCompile] || !stages[domain.StageProjectTest] ||
		!toolchains["typescript/tsc"] || !toolchains["go/test"] || len(fingerprints) != 2 {
		t.Fatalf("stages=%v toolchains=%v fingerprints=%v", stages, toolchains, fingerprints)
	}

	batches, _, err := (&Batcher{DB: db, Ident: ident, Cfg: config.Default()}).build(context.Background())
	if err != nil {
		t.Fatalf("build batches: %v", err)
	}
	for _, batch := range batches {
		if batch.OuterCommand != "go test" || batch.ActualToolchain == "" || batch.StageEvidence == "" {
			t.Errorf("batch lost failure lineage: %+v", batch)
		}
	}
}

func TestRecordRunUnknownCommandRecordsUsedPassOnly(t *testing.T) {
	db := testDB(t)
	rec := &Recorder{DB: db, Ident: testIdentity(t), Cfg: config.Default()}

	// Even a failing unknown command proves nothing beyond usage.
	if err := rec.RecordRun(context.Background(), t.TempDir(), fakeScanResult(), scanner.CommandProfile{}, 9, "some error"); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	rows := pendingRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 USED row, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.PURL != "pkg:npm/axios@1.12.0" || r.Symbol != "" {
		t.Fatalf("unexpected row: %+v", r)
	}
	if r.Stage != domain.StageUsed || r.Result != domain.ResultPass {
		t.Fatalf("stage/result = %s/%s, want USED/PASS", r.Stage, r.Result)
	}
	if r.ErrorFP != "" || r.ErrorCode != "" {
		t.Fatalf("USED row carries error info: %+v", r)
	}
}

func TestRecordRunStoresProjectBucketSightings(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	rec := &Recorder{DB: db, Ident: ident, Cfg: config.Default()}
	dir := t.TempDir()

	if err := rec.RecordRun(context.Background(), dir, fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	month := time.Now().UTC().Format("2006-01")
	abs, _ := filepath.Abs(dir)
	want := ident.ProjectBucket(abs, month)
	usages, err := db.SymbolUsages(context.Background(), domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"})
	if err != nil {
		t.Fatalf("SymbolUsages: %v", err)
	}
	if len(usages) == 0 {
		t.Fatal("no symbol sightings recorded")
	}
	for _, u := range usages {
		if u.ProjectBucket != want {
			t.Errorf("project bucket = %q, want %q", u.ProjectBucket, want)
		}
		if strings.Contains(u.ProjectBucket, string(filepath.Separator)) {
			t.Errorf("project bucket looks like a path: %q", u.ProjectBucket)
		}
	}
}

func TestRecordCommandOutputWiresCLIPassAndFailExperience(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}

	exit0 := 0
	outputPass := CommandOutput{
		Stdout:      "Container started\n",
		Termination: domain.FailureTermination{Kind: "", ExitCode: &exit0},
	}
	err := rec.RecordCommandOutput(context.Background(), t.TempDir(), nil, scanner.CommandProfile{},
		[]string{"docker", "compose", "up", "-d"}, 0, outputPass)
	if err != nil {
		t.Fatalf("RecordCommandOutput pass: %v", err)
	}

	coord := domain.CLIExperienceCoordinate{
		Tool:        "docker",
		Subcommand:  "compose up",
		ArgsPattern: "-d",
	}
	summary, err := db.QueryCLIExperience(context.Background(), coord)
	if err != nil {
		t.Fatalf("QueryCLIExperience: %v", err)
	}
	if summary.FieldPassCount != 1 {
		t.Errorf("FieldPassCount = %d, want 1", summary.FieldPassCount)
	}

	// Verify failure observation for a known build profile (e.g., go test with StageProjectTest).
	// Previously dropped due to restrictive !profile.Known || profile.Stage == StageProjectProcess gate.
	exit1 := 1
	outputGoFail := CommandOutput{
		Stderr:      "--- FAIL: TestFoo (0.01s)\n    foo_test.go:12: assertion failed\nFAIL\n",
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit1},
	}
	goProfile := scanner.CommandProfile{
		Stage: domain.StageProjectTest,
		Known: true,
		Tool:  "go",
	}
	err = rec.RecordCommandOutput(context.Background(), t.TempDir(), nil, goProfile,
		[]string{"go", "test"}, 1, outputGoFail)
	if err != nil {
		t.Fatalf("RecordCommandOutput go fail: %v", err)
	}

	goCoord := domain.ParseCLICommand([]string{"go", "test"}, domain.EnvironmentFingerprint{})
	goSummary, err := db.QueryCLIExperience(context.Background(), goCoord)
	if err != nil {
		t.Fatalf("QueryCLIExperience go fail: %v", err)
	}
	if goSummary.FieldFailCount != 1 {
		t.Errorf("FieldFailCount = %d, want 1", goSummary.FieldFailCount)
	}
	if goSummary.Status != "OBSERVED_FAIL" {
		t.Errorf("Status = %q, want OBSERVED_FAIL", goSummary.Status)
	}
	if len(goSummary.RecentFailures) == 0 {
		t.Fatalf("expected RecentFailures to be populated")
	}
	if goSummary.RecentFailures[0].Result != domain.ResultFail {
		t.Errorf("RecentFailures[0].Result = %q, want %q", goSummary.RecentFailures[0].Result, domain.ResultFail)
	}

	var classified *localdb.ObsRow
	for _, row := range pendingRows(t, db) {
		if row.Result == domain.ResultFail && row.PURL == "pkg:generic/cli/go@0.0.0" {
			row := row
			classified = &row
			break
		}
	}
	if classified == nil {
		t.Fatal("classified go failure observation was not persisted")
	}
	if classified.Stage != domain.StageProjectTest || classified.OuterStage != domain.StageProjectTest ||
		classified.ActualToolchain != "go/test" || classified.StageEvidence != domain.FailureStageTestRunnerDiagnostic ||
		classified.FailureEvidenceGap != "" {
		t.Fatalf("classified failure lineage was not preserved: %+v", *classified)
	}
	wantFingerprint := domain.ClassifiedFailureFingerprint(classified.Stage, classified.ActualToolchain,
		classifiedTermination(*classified), classified.ErrorCode, classified.ErrorSummary)
	if classified.ErrorFP != wantFingerprint {
		t.Fatalf("classified fingerprint = %q, want %q", classified.ErrorFP, wantFingerprint)
	}

	// Record a subsequent pass for the same coordinate to verify coexisting boundary without survivorship bias
	outputGoPass := CommandOutput{
		Stdout:      "PASS\n",
		Termination: domain.FailureTermination{Kind: "", ExitCode: &exit0},
	}
	err = rec.RecordCommandOutput(context.Background(), t.TempDir(), nil, goProfile,
		[]string{"go", "test"}, 0, outputGoPass)
	if err != nil {
		t.Fatalf("RecordCommandOutput go pass: %v", err)
	}

	goSummaryCoexist, err := db.QueryCLIExperience(context.Background(), goCoord)
	if err != nil {
		t.Fatalf("QueryCLIExperience go coexist: %v", err)
	}
	if goSummaryCoexist.FieldPassCount != 1 || goSummaryCoexist.FieldFailCount != 1 {
		t.Errorf("got %d PASS, %d FAIL; want 1 PASS, 1 FAIL", goSummaryCoexist.FieldPassCount, goSummaryCoexist.FieldFailCount)
	}
	if goSummaryCoexist.Status != "COEXISTING_BOUNDARY" {
		t.Errorf("Status = %q, want COEXISTING_BOUNDARY", goSummaryCoexist.Status)
	}
}

func TestRecordCommandOutputClassifiedBatchPassesServerValidation(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	ctx := context.Background()
	env := testEnvFP()
	exitCode := 1
	started := time.Date(2026, 9, 11, 7, 0, 0, 0, time.UTC)
	output := CommandOutput{
		Stderr:      "AssertionError: expected true\n",
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode},
		ToolVersion: "11.5.2",
		Shell:       "direct",
		StartedAt:   started,
		FinishedAt:  started.Add(time.Second),
	}
	profile := scanner.CommandProfile{Stage: domain.StageProjectTest, Known: true, Tool: "npm"}
	if err := rec.RecordCommandOutput(ctx, t.TempDir(), &scanner.ScanResult{Env: env}, profile,
		[]string{"npm", "test"}, exitCode, output); err != nil {
		t.Fatalf("RecordCommandOutput: %v", err)
	}

	rows := pendingRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("pending rows = %d, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	cliPURL, err := domain.ParsePURL(row.PURL)
	if err != nil {
		t.Fatalf("parse classified CLI purl: %v", err)
	}
	if err := db.RecordSymbolUsage(ctx, cliPURL, row.Symbol, domain.SymbolUnknown, "classified-cli-test"); err != nil {
		t.Fatalf("record project bucket source: %v", err)
	}

	batches, _, err := (&Batcher{DB: db, Ident: ident, Cfg: cfg}).build(ctx)
	if err != nil {
		t.Fatalf("build batches: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1: %+v", len(batches), batches)
	}
	batch := batches[0]
	if batch.Stage != domain.StageProjectTest || batch.OuterStage != domain.StageProjectTest ||
		batch.ActualToolchain != "javascript/test-runner" || batch.StageEvidence != domain.FailureStageTestRunnerDiagnostic {
		t.Fatalf("batch lost classified failure lineage: %+v", batch)
	}
	if err := serverstore.ValidateBatch(batch); err != nil {
		t.Fatalf("ValidateBatch rejected classified CLI failure: %v\nbatch: %+v", err, batch)
	}
}

func classifiedTermination(row localdb.ObsRow) domain.FailureTermination {
	return domain.FailureTermination{
		Kind: row.TerminationKind, ExitCode: row.ExitCode, Signal: row.Signal, TimeoutMillis: row.TimeoutMillis,
	}
}

func TestRecordCommandOutputRecordsKnownBuildTestCompileFailures(t *testing.T) {
	cases := []struct {
		name       string
		profile    scanner.CommandProfile
		argv       []string
		output     CommandOutput
		wantTool   string
		wantSubcmd string
		wantArgs   string
	}{
		{
			name: "npm run build compile failure",
			profile: scanner.CommandProfile{
				Stage: domain.StageProjectCompile,
				Known: true,
				Tool:  "npm",
			},
			argv: []string{"npm", "run", "build"},
			output: CommandOutput{
				Stderr: "error TS2304: Cannot find name 'MissingType'.\n",
			},
			wantTool:   "npm",
			wantSubcmd: "run build",
			wantArgs:   "",
		},
		{
			name: "cargo test failure",
			profile: scanner.CommandProfile{
				Stage: domain.StageProjectTest,
				Known: true,
				Tool:  "cargo",
			},
			argv: []string{"cargo", "test"},
			output: CommandOutput{
				Stderr: "thread 'main' panicked at 'assertion failed: `(left == right)`'\n",
			},
			wantTool:   "cargo",
			wantSubcmd: "test",
			wantArgs:   "",
		},
		{
			name: "tsc typecheck failure",
			profile: scanner.CommandProfile{
				Stage: domain.StageProjectTypecheck,
				Known: true,
				Tool:  "tsc",
			},
			argv: []string{"tsc"},
			output: CommandOutput{
				Stderr: "src/index.ts(1,1): error TS2304: Cannot find name 'x'.\n",
			},
			wantTool:   "tsc",
			wantSubcmd: "",
			wantArgs:   "",
		},
		{
			name: "pytest test failure",
			profile: scanner.CommandProfile{
				Stage: domain.StageProjectTest,
				Known: true,
				Tool:  "pytest",
			},
			argv: []string{"pytest"},
			output: CommandOutput{
				Stderr: "FAILED test_sample.py::test_answer - AssertionError: assert 3 == 5\n",
			},
			wantTool:   "pytest",
			wantSubcmd: "",
			wantArgs:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			ident := testIdentity(t)
			cfg := config.Default()
			cfg.Mode = config.ModeCommunity
			rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}

			exit1 := 1
			out := tc.output
			out.Termination = domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit1}

			err := rec.RecordCommandOutput(context.Background(), t.TempDir(), nil, tc.profile, tc.argv, 1, out)
			if err != nil {
				t.Fatalf("RecordCommandOutput: %v", err)
			}

			coord := domain.CLIExperienceCoordinate{
				Tool:        tc.wantTool,
				Subcommand:  tc.wantSubcmd,
				ArgsPattern: tc.wantArgs,
			}
			summary, err := db.QueryCLIExperience(context.Background(), coord)
			if err != nil {
				t.Fatalf("QueryCLIExperience: %v", err)
			}
			if summary.FieldFailCount != 1 {
				t.Errorf("FieldFailCount = %d, want 1", summary.FieldFailCount)
			}
			if summary.Status != "OBSERVED_FAIL" {
				t.Errorf("Status = %q, want OBSERVED_FAIL", summary.Status)
			}
			if len(summary.RecentFailures) != 1 {
				t.Fatalf("RecentFailures count = %d, want 1", len(summary.RecentFailures))
			}
			if summary.RecentFailures[0].Result != domain.ResultFail {
				t.Errorf("RecentFailures[0].Result = %q, want %q", summary.RecentFailures[0].Result, domain.ResultFail)
			}

			evidenceRows, err := db.ListCLIExecutionEvidence(context.Background(), coord, 10)
			if err != nil {
				t.Fatalf("ListCLIExecutionEvidence: %v", err)
			}
			if len(evidenceRows) != 1 {
				t.Fatalf("ListCLIExecutionEvidence count = %d, want 1", len(evidenceRows))
			}
			if !evidenceRows[0].IsHighInformation {
				t.Errorf("expected failure evidence to be high information")
			}
		})
	}
}

func TestRecordCommandOutputStoresOnlyStructuredSecretSafeCLIEvidence(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}

	env := domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "windows", Arch: "x64", Runtime: "go", RuntimeVersion: "1.26"}
	started := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	exit := 7
	output := CommandOutput{
		Stdout:      "AcmeRoadmap secret-roadmap.txt at https://private.example/token ghp_12345678901234567890\nline two\nline three\nline four\nline five\n",
		Stderr:      "open C:\\Users\\Alice\\secret.txt failed\nAuthorization: Bearer shortsecret\npassword: correct horse battery staple\n",
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit},
		ToolVersion: "2.55.0",
		Shell:       "direct",
		StartedAt:   started,
		FinishedAt:  finished,
	}
	if err := rec.RecordCommandOutput(context.Background(), t.TempDir(), &scanner.ScanResult{Env: env},
		scanner.CommandProfile{}, []string{"git", "status", "--short"}, exit, output); err != nil {
		t.Fatalf("RecordCommandOutput: %v", err)
	}

	coord := domain.ParseCLICommand([]string{"git", "status", "--short"}, env)
	coord.ToolVersion = "2.55.0"
	coord.Shell = "direct"
	rows, err := db.ListCLIExecutionEvidence(context.Background(), coord, 10)
	if err != nil {
		t.Fatalf("ListCLIExecutionEvidence: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("structured evidence rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.ID == "" {
		t.Fatal("evidence ID was not returned")
	}
	if !got.IsHighInformation {
		t.Fatal("failure evidence was not marked isHighInformation")
	}
	if got.EvidenceQuality != domain.EvidenceComplete || got.EnvironmentID != env.Hash() {
		t.Fatalf("quality/environment = %q/%q", got.EvidenceQuality, got.EnvironmentID)
	}
	if got.StartedAt != started.Format(time.RFC3339Nano) || got.FinishedAt != finished.Format(time.RFC3339Nano) {
		t.Fatalf("execution window = %q .. %q", got.StartedAt, got.FinishedAt)
	}
	combined := got.Stdout.Excerpt + " " + got.Stderr.Excerpt
	for _, secret := range []string{"AcmeRoadmap", "secret-roadmap.txt", "private.example", "Alice", "ghp_", "secret.txt", "shortsecret", "horse battery staple"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("secret %q survived in structured excerpts: %q", secret, combined)
		}
	}
	if got.Stdout.Fingerprint == "" || got.Stderr.Fingerprint == "" {
		t.Fatalf("stream fingerprints missing: stdout=%q stderr=%q", got.Stdout.Fingerprint, got.Stderr.Fingerprint)
	}
	if !got.Stdout.Truncated {
		t.Fatal("five-line stdout excerpt was not marked truncated at the four-line evidence bound")
	}
}
