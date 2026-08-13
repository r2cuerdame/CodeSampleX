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
