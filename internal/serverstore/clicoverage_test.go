package serverstore

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var cliNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func cliObs(tool, version, provenance, command, os, result string, count int64) CLIObservationRow {
	return CLIObservationRow{
		PURL:     "pkg:generic/cli/" + tool + "@" + version,
		Symbol:   domain.EncodeCLISymbol("", command, domain.ExperienceProvenance(provenance)),
		OS:       os,
		Result:   result,
		Count:    count,
		LastSeen: cliNow.Add(-time.Hour),
	}
}

func gapKeys(rows []WantedRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name+"@"+r.Version+" "+r.Symbol)
	}
	return out
}

func i64(v int64) string { return strconv.FormatInt(v, 10) }

// With nothing observed, the farm's first work is the seed probes, in seed
// order, one per farm OS. A probe has no version: it exists to learn one.
func TestCLIPlanSeedsProbesInPriorityOrder(t *testing.T) {
	plan := PlanCLICoverage(nil, nil, []string{"linux", "windows"}, cliNow, 100)
	if len(plan.Gaps) != 2*len(domain.CLISeedTools) {
		t.Fatalf("gaps = %v", gapKeys(plan.Gaps))
	}
	for i, tool := range domain.CLISeedTools {
		for j, os := range []string{"linux", "windows"} {
			row := plan.Gaps[i*2+j]
			if row.Ecosystem != "generic" || row.Name != "cli/"+tool || row.Version != "" ||
				row.Symbol != domain.EncodeCLIWorkSymbol(os, "") || row.Kind != "CLI" ||
				row.Axis != AuthoringAxisEvidence || row.TargetOS != os {
				t.Fatalf("gap %d = %+v", i*2+j, row)
			}
		}
	}
	// Seed order is carried as score so the request-first ranking keeps it.
	if plan.Gaps[0].Score <= plan.Gaps[2].Score || plan.Gaps[len(plan.Gaps)-1].Score != 1 {
		t.Fatalf("seed scores do not descend in seed order: %+v", plan.Gaps)
	}
	if plan.Census.Queued != len(plan.Gaps) || plan.Census.Observed != 0 {
		t.Fatalf("census = %+v", plan.Census)
	}
}

// A tool outside the seed list is probed only once the network has heard of
// it -- an observation or an ask -- so sixty tools do not become a hundred
// and twenty probes on day one.
func TestCLIPlanProbesUnseededToolsOnlyOnceSeen(t *testing.T) {
	plan := PlanCLICoverage(nil, nil, []string{"linux"}, cliNow, 100)
	for _, key := range gapKeys(plan.Gaps) {
		if strings.HasPrefix(key, "cli/jq@") {
			t.Fatalf("jq probed with nothing observed: %v", key)
		}
	}
	plan = PlanCLICoverage([]CLIObservationRow{cliObs("jq", "1.7.1", "field", "-r <arg>", "windows", "PASS", 3)}, nil, []string{"linux"}, cliNow, 100)
	found := false
	for _, row := range plan.Gaps {
		if row.Name == "cli/jq" && row.Symbol == "[linux]" && row.Version == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("jq observed on windows but not probed on linux: %v", gapKeys(plan.Gaps))
	}
}

// Once the farm has probed a tool on an OS, the commands the network has seen
// anywhere become gaps at the version the farm actually has there. Commands
// already observed at that exact coordinate are not gaps, and the probe is
// finished.
func TestCLIPlanFillsCommandsAtTheFarmsOwnVersion(t *testing.T) {
	obs := []CLIObservationRow{
		cliObs("git", "2.47.2", "farm", "", "linux", "PASS", 1),
		cliObs("git", "2.51.0", "field", "worktree add <path>", "windows", "PASS", 4),
		cliObs("git", "2.51.0", "field", "status --short", "windows", "PASS", 9),
		cliObs("git", "2.47.2", "field", "status --short", "linux", "PASS", 2),
	}
	plan := PlanCLICoverage(obs, nil, []string{"linux"}, cliNow, 100)
	var git []string
	for _, row := range plan.Gaps {
		if row.Name == "cli/git" {
			git = append(git, row.Version+" "+row.Symbol)
		}
	}
	if len(git) != 1 || git[0] != "2.47.2 [linux] worktree add <path>" {
		t.Fatalf("git gaps = %v", git)
	}
	tool := plan.Census.toolNamed("git")
	if tool.FarmVersions["linux"] != "2.47.2" || tool.Observed != 4 || tool.FarmObserved != 1 || tool.Queued != 1 {
		t.Fatalf("git census = %+v", tool)
	}
}

// Repeated failures and version boundaries are the commands worth a farm run
// first: a failure nobody has explained outranks a command that always
// passed, and a command whose verdict flipped between versions outranks one
// that never did.
func TestCLIPlanPrioritizesFailuresAndVersionBoundaries(t *testing.T) {
	obs := []CLIObservationRow{
		cliObs("npm", "11.5.2", "farm", "", "linux", "PASS", 1),
		cliObs("npm", "11.5.2", "field", "run build", "windows", "PASS", 50),
		cliObs("npm", "11.5.2", "field", "ci", "windows", "FAIL", 2),
		cliObs("npm", "10.9.0", "field", "install <arg>", "windows", "PASS", 5),
		cliObs("npm", "11.5.2", "field", "install <arg>", "windows", "FAIL", 1),
	}
	plan := PlanCLICoverage(obs, nil, []string{"linux"}, cliNow, 100)
	var npm []string
	for _, row := range plan.Gaps {
		if row.Name == "cli/npm" {
			npm = append(npm, row.Symbol)
		}
	}
	want := []string{"[linux] install <arg>", "[linux] ci", "[linux] run build"}
	if strings.Join(npm, ",") != strings.Join(want, ",") {
		t.Fatalf("npm gaps = %v, want %v", npm, want)
	}
	tool := plan.Census.toolNamed("npm")
	if tool.Failures != 2 || tool.Boundaries != 1 {
		t.Fatalf("npm census = %+v", tool)
	}
}

// An OS the farm has no lane for is not a gap the farm can fill. It is
// counted and named, never queued -- a macOS coordinate handed to a Linux
// worker would come back as a failure of the tool.
func TestCLIPlanClassifiesEnvironmentOnlyGapsWithoutQueuingThem(t *testing.T) {
	obs := []CLIObservationRow{
		cliObs("gh", "2.78.0", "field", "pr view <arg>", "darwin", "FAIL", 3),
		cliObs("gh", "2.78.0", "field", "pr view <arg>", "macos", "FAIL", 3),
	}
	wanted := []WantedRow{
		{Ecosystem: "generic", Name: "cli/gh", Version: "2.78.0", Symbol: "pr view <arg>", Asks: 2, TargetOS: "macos"},
	}
	plan := PlanCLICoverage(obs, wanted, []string{"linux"}, cliNow, 100)
	for _, row := range plan.Gaps {
		if row.Name == "cli/gh" && row.Symbol != "[linux]" {
			t.Fatalf("a macOS-only coordinate was queued: %+v", row)
		}
	}
	if plan.Census.Unavailable != 2 || plan.Census.Unavailability["no farm lane for darwin"] != 1 ||
		plan.Census.Unavailability["no farm lane for macos"] != 1 {
		t.Fatalf("census = %+v", plan.Census)
	}
}

// An explicit ask is queued at the version it asks for when the farm has
// that version, and classified as unavailable -- with the version the farm
// does have named -- when it does not. Neither is silently rewritten.
func TestCLIPlanHonoursWantedVersionsAgainstTheFarmsVersion(t *testing.T) {
	obs := []CLIObservationRow{cliObs("gh", "2.76.0", "farm", "", "linux", "PASS", 1)}
	wanted := []WantedRow{
		{Ecosystem: "generic", Name: "cli/gh", Version: "2.78.0", Symbol: "gh run watch <arg>", Asks: 3},
		{Ecosystem: "generic", Name: "cli/gh", Version: "2.76.0", Symbol: "pr list", Asks: 1},
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Asks: 9},
	}
	plan := PlanCLICoverage(obs, wanted, []string{"linux", "windows"}, cliNow, 100)
	var gh []string
	for _, row := range plan.Gaps {
		if row.Name == "cli/gh" {
			gh = append(gh, row.Version+" "+row.Symbol+" asks="+i64(row.Asks))
		}
	}
	// The windows probe still has to happen; pr list is fillable on linux;
	// run watch at 2.78.0 is not, and is not queued.
	want := []string{"2.76.0 [linux] pr list asks=1", " [windows] asks=0"}
	if strings.Join(gh, ";") != strings.Join(want, ";") {
		t.Fatalf("gh gaps = %v, want %v", gh, want)
	}
	if plan.Census.Unavailability["farm provides another version"] != 1 {
		t.Fatalf("census = %+v", plan.Census)
	}
}

// The plan is bounded on every axis a runaway could come from: commands per
// (tool, OS), and rows overall.
func TestCLIPlanIsBounded(t *testing.T) {
	obs := []CLIObservationRow{cliObs("git", "2.47.2", "farm", "", "linux", "PASS", 1)}
	for i := 0; i < 40; i++ {
		obs = append(obs, cliObs("git", "2.51.0", "field", "log --oneline -"+itoa(i), "windows", "PASS", int64(i)))
	}
	plan := PlanCLICoverage(obs, nil, []string{"linux"}, cliNow, 1000)
	git := 0
	for _, row := range plan.Gaps {
		if row.Name == "cli/git" {
			git++
		}
	}
	if git != CLICommandsPerToolOS {
		t.Fatalf("git rows = %d, want the per-tool cap %d", git, CLICommandsPerToolOS)
	}
	if plan := PlanCLICoverage(obs, nil, []string{"linux"}, cliNow, 3); len(plan.Gaps) != 3 {
		t.Fatalf("limit ignored: %d rows", len(plan.Gaps))
	}
}

// A farm version is a fact about the farm's image on the day it was probed.
// Images move, so a probe older than the reprobe window is asked again.
func TestCLIPlanReprobesAStaleFarmVersion(t *testing.T) {
	stale := cliObs("go", "1.25.0", "farm", "", "linux", "PASS", 1)
	stale.LastSeen = cliNow.Add(-CLIFarmReprobeAfter - time.Hour)
	plan := PlanCLICoverage([]CLIObservationRow{stale}, nil, []string{"linux"}, cliNow, 100)
	for _, row := range plan.Gaps {
		if row.Name == "cli/go" && row.Symbol == "[linux]" && row.Version == "" {
			return
		}
	}
	t.Fatalf("stale go probe was not scheduled again: %v", gapKeys(plan.Gaps))
}

// The Fake and PostgreSQL read the same rows; the Fake half is checked here
// against ingest, the PostgreSQL half in the parity suite.
func TestFakeListsCLIObservationsFromIngestedBatches(t *testing.T) {
	f := NewFake()
	code := 0
	b := domain.ObservationBatch{
		SchemaVersion: 2, Epoch: "2026-09-18", AnonID: "0123456789abcdef0123456789abcdef",
		ProjectBucket: "0123456789abcdef0123456789abcdef",
		Package:       "pkg:generic/cli/git@2.47.2", Symbol: "farm:status --short",
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "linux", Arch: "amd64"},
		Stage:       domain.StageProjectProcess, Result: domain.ResultPass, ObservationCount: 2,
		TerminationKind: domain.TerminationExit, ExitCode: &code, ActualToolchain: "farm",
	}
	if accepted, rejected, err := f.IngestBatches(context.Background(), []domain.ObservationBatch{b}); err != nil || accepted != 1 {
		t.Fatalf("ingest: accepted=%d rejected=%+v err=%v", accepted, rejected, err)
	}
	rows, err := f.ListCLIObservations(context.Background(), 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].PURL != "pkg:generic/cli/git@2.47.2" || rows[0].Symbol != "farm:status --short" ||
		rows[0].OS != "linux" || rows[0].Result != "PASS" || rows[0].Count != 2 || rows[0].LastSeen.IsZero() {
		t.Fatalf("row = %+v", rows[0])
	}
}
