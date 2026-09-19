package fixclaims

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func writeRepro(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readManifest(t *testing.T, dir string) domain.SampleManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "csx.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m domain.SampleManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const reproManifestNPM = `{"schemaVersion":1,"packages":["pkg:npm/express@4.22.2","pkg:npm/supertest@7.1.0"],"symbols":["req.query"],
"case":{"schemaVersion":1,"kind":"FIX","goal":"g","caseId":"case:sha256:stale","packages":["pkg:npm/express@4.22.2"],"contract":["c"]},
"environment":{"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"x64","runtime":"node","runtimeVersion":"22"},"contractCommand":["node","test.mjs"],"license":"MIT-0","verifierAdapter":"node-typescript@1"}`

func TestRepinRewritesOnlyTheCandidatePackageInNPM(t *testing.T) {
	dir := writeRepro(t, map[string]string{
		"csx.json":     reproManifestNPM,
		"package.json": `{"name":"r","private":true,"dependencies":{"express":"4.22.2","supertest":"7.1.0"},"devDependencies":{"express":"4.22.2"}}`,
	})
	c := Candidate{Ecosystem: "npm", Name: "express", ClaimedBadVersion: "4.22.1", ClaimedFixedVersion: "4.22.2"}
	if err := Repin(dir, c, "4.22.1", Environment{OS: "linux"}); err != nil {
		t.Fatal(err)
	}
	m := readManifest(t, dir)
	if got := strings.Join(m.Packages, ","); got != "pkg:npm/express@4.22.1,pkg:npm/supertest@7.1.0" {
		t.Fatalf("packages: %s", got)
	}
	if got := strings.Join(m.Case.Packages, ","); got != "pkg:npm/express@4.22.1" {
		t.Fatalf("case.packages: %s", got)
	}
	if m.Case.CaseID != "" {
		t.Fatalf("stale case id survived: %s", m.Case.CaseID)
	}
	if m.Environment.RuntimeVersion != "22" {
		t.Fatalf("runtime version changed without a probe asking: %s", m.Environment.RuntimeVersion)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Private         bool              `json:"private"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Dependencies["express"] != "4.22.1" || pkg.DevDependencies["express"] != "4.22.1" || pkg.Dependencies["supertest"] != "7.1.0" || !pkg.Private {
		t.Fatalf("package.json: %s", raw)
	}
}

func TestRepinDeclaresTheProbeRuntimeVersion(t *testing.T) {
	dir := writeRepro(t, map[string]string{
		"csx.json": strings.NewReplacer(`"ecosystem":"npm"`, `"ecosystem":"pypi"`, `"runtime":"node","runtimeVersion":"22"`, `"runtime":"python","runtimeVersion":"3.12","languageVersion":"3.12"`,
			"pkg:npm/express@4.22.2", "pkg:pypi/pydantic@2.13.3", "pkg:npm/supertest@7.1.0", "pkg:pypi/typing-extensions@4.15.0").Replace(reproManifestNPM),
		"requirements.txt": "Pydantic==2.13.3\ntyping_extensions==4.15.0\n",
	})
	c := Candidate{Ecosystem: "pypi", Name: "pydantic", ClaimedFixedVersion: "2.13.3"}
	if err := Repin(dir, c, "2.13.2", Environment{OS: "linux", Runtime: "python", RuntimeVersion: "3.14"}); err != nil {
		t.Fatal(err)
	}
	m := readManifest(t, dir)
	if m.Environment.RuntimeVersion != "3.14" || m.Environment.LanguageVersion != "3.14" {
		t.Fatalf("environment: %+v", m.Environment)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "requirements.txt"))
	if string(raw) != "Pydantic==2.13.2\ntyping_extensions==4.15.0\n" {
		t.Fatalf("requirements: %q", raw)
	}
}

func TestRepinCargoAndGoUseExactPins(t *testing.T) {
	cargo := writeRepro(t, map[string]string{
		"csx.json": strings.NewReplacer(`"ecosystem":"npm"`, `"ecosystem":"cargo"`, "pkg:npm/express@4.22.2", "pkg:cargo/tokio@1.52.3", "pkg:npm/supertest@7.1.0", "pkg:cargo/tokio-util@0.7.0").Replace(reproManifestNPM),
		"Cargo.toml": "[package]\nname = \"r\"\n\n[dependencies]\ntokio = { version = \"1.52.3\", features = [\n  \"rt\",\n] }\ntokio-util = \"0.7.0\"\n",
	})
	if err := Repin(cargo, Candidate{Ecosystem: "cargo", Name: "tokio", ClaimedFixedVersion: "1.52.3"}, "1.51.4", Environment{OS: "linux"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(cargo, "Cargo.toml"))
	if !strings.Contains(string(raw), `tokio = { version = "=1.51.4", features = [`) || !strings.Contains(string(raw), `tokio-util = "0.7.0"`) {
		t.Fatalf("Cargo.toml: %s", raw)
	}

	gomod := writeRepro(t, map[string]string{
		"csx.json": strings.NewReplacer(`"ecosystem":"npm"`, `"ecosystem":"golang"`, "pkg:npm/express@4.22.2", "pkg:golang/github.com/spf13/cobra@v1.10.0", "pkg:npm/supertest@7.1.0", "pkg:golang/github.com/spf13/pflag@v1.0.9").Replace(reproManifestNPM),
		"go.mod":   "module r\n\ngo 1.24\n\nrequire github.com/spf13/cobra v1.10.0\n\nrequire github.com/spf13/pflag v1.0.9 // indirect\n",
	})
	if err := Repin(gomod, Candidate{Ecosystem: "golang", Name: "github.com/spf13/cobra", ClaimedFixedVersion: "1.10.0"}, "1.9.1", Environment{OS: "linux"}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(gomod, "go.mod"))
	if !strings.Contains(string(raw), "require github.com/spf13/cobra v1.9.1\n") || !strings.Contains(string(raw), "pflag v1.0.9") {
		t.Fatalf("go.mod: %s", raw)
	}
	if m := readManifest(t, gomod); m.Packages[0] != "pkg:golang/github.com/spf13/cobra@v1.9.1" {
		t.Fatalf("packages: %v", m.Packages)
	}
}

func TestRepinRefusesAReproducerThatDoesNotNameThePackage(t *testing.T) {
	dir := writeRepro(t, map[string]string{"csx.json": reproManifestNPM, "package.json": `{"dependencies":{"express":"4.22.2"}}`})
	err := Repin(dir, Candidate{Ecosystem: "npm", Name: "axios", ClaimedFixedVersion: "1.18.1"}, "1.18.0", Environment{OS: "linux"})
	if err == nil || !strings.Contains(err.Error(), "does not name pkg:npm/axios") {
		t.Fatalf("err: %v", err)
	}
}

func TestRelockRunsTheEcosystemToolAndDropsAStaleCargoLock(t *testing.T) {
	old := execLock
	t.Cleanup(func() { execLock = old })
	var ran [][]string
	execLock = func(_ context.Context, dir string, argv []string) ([]byte, error) {
		ran = append(ran, argv)
		if argv[0] == "go" {
			return []byte("go: module x: not found"), errors.New("exit 1")
		}
		return nil, nil
	}
	dir := writeRepro(t, map[string]string{"Cargo.lock": "stale"})
	if err := Relock(context.Background(), dir, "cargo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Cargo.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale Cargo.lock survived")
	}
	if err := Relock(context.Background(), dir, "pypi"); err != nil {
		t.Fatal(err)
	}
	err := Relock(context.Background(), dir, "golang")
	if err == nil || !strings.Contains(err.Error(), "go mod tidy") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err: %v", err)
	}
	if len(ran) != 2 || ran[0][0] != "cargo" || ran[1][0] != "go" {
		t.Fatalf("ran: %v", ran)
	}
}

func TestPlanAddsThePairForAVersionedRuntimeHint(t *testing.T) {
	c := Candidate{Ecosystem: "pypi", Name: "pydantic", ClaimedBadVersion: "2.13.2", ClaimedFixedVersion: "2.13.3",
		EnvironmentHints: []string{"python-3.12", "python-3.14", "python", "python-3.12"}}
	probes := Plan(c, nil, nil, Policy{MaxRuns: 12, MaxProbesPerTurn: 8})
	var keys []string
	for _, p := range probes {
		keys = append(keys, p.Environment.Key()+"@"+p.Version+":"+p.Reason)
	}
	want := "linux|||@2.13.2:bad-version,linux|||@2.13.3:fixed-version," +
		"linux||python|3.12@2.13.2:environment-hint,linux||python|3.12@2.13.3:environment-hint," +
		"linux||python|3.14@2.13.2:environment-hint,linux||python|3.14@2.13.3:environment-hint"
	if got := strings.Join(keys, ","); got != want {
		t.Fatalf("probes:\n got %s\nwant %s", got, want)
	}
}
