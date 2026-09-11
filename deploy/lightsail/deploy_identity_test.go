package lightsail

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const identityTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const identityTestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const identityTestCanary = "CSX_PRIVATE_IDENTITY_CANARY=must-never-be-reported"

var identityTestStages = []string{"deploy-directory", "container-revision", "container-image", "image-revision", "health", "served-revision"}

func identityTestEvidence() string {
	return "revision=" + identityTestSHA + "\nimage_digest=" + identityTestDigest +
		"\nimage_revision=" + identityTestSHA + "\nhealth=ok\nserved_revision=" + identityTestSHA + "\n"
}

func identityTestShell(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil && runtime.GOOS == "windows" {
		sh, err = exec.LookPath(`C:\Program Files\Git\bin\sh.exe`)
	}
	if err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	return sh
}

// Read the actual wrapper framing and remote command, so this test catches a
// regression in the production transport as well as in the collector itself.
func identityTestTransport(t *testing.T) (string, string, string) {
	t.Helper()
	script := readDeployFixture(t, "deploy-production.ps1")
	framing := regexp.MustCompile(`GetBytes\("([^"\r\n]+)" \+ \[IO.File\]::ReadAllText\(\$collector\) \+ "([^"\r\n]+)"\)`).FindStringSubmatch(script)
	remote := regexp.MustCompile(`"(\{ printf '#'; cat; \} \| timeout[^"\r\n]+ sh)"`).FindStringSubmatch(script)
	if len(framing) != 3 || len(remote) != 2 {
		t.Fatal("cannot locate production identity framing and remote command")
	}
	decode := strings.NewReplacer("`n", "\n", "`r", "\r")
	command := remote[1]
	if runtime.GOOS == "windows" {
		command = "PATH='/usr/bin':$PATH\n" + command
	}
	return command, decode.Replace(framing[1]), decode.Replace(framing[2])
}

func TestDeployIdentityTransportCannotConsumeItsOwnScript(t *testing.T) {
	sh := identityTestShell(t)
	remote, prefix, suffix := identityTestTransport(t)
	// More than a pipe/shell input buffer must remain after a child reads stdin.
	body := "printf 'before\\n'\ncat >/dev/null\n" + strings.Repeat("# unread identity collector padding\n", 8192) + "printf 'after\\n'\n"
	for _, tc := range []struct{ name, bom string }{{"plain", ""}, {"bom", "\xef\xbb\xbf"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, sh, "-c", remote)
			cmd.Stdin = strings.NewReader(tc.bom + prefix + body + suffix)
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != "before\nafter\n" {
				t.Fatalf("identity source was consumed: err=%v output=%q", err, out)
			}
		})
	}
}

func TestDeployIdentityCollectorPreservesEvidenceAndProbeFailures(t *testing.T) {
	sh := identityTestShell(t)
	remote, prefix, suffix := identityTestTransport(t)
	collector := readDeployFixture(t, "collect-deploy-identity.sh")
	if !strings.Contains(collector, "\nimage_digest=") {
		t.Fatal("cannot place long-stream fixture before the remaining identity probes")
	}
	collector = strings.Replace(collector, "\nimage_digest=", "\n"+strings.Repeat("# identity source beyond stdin buffer\n", 8192)+"image_digest=", 1)
	// These local shell functions are the only Docker and deployment-directory
	// adapters. They neither inspect the real machine nor make network requests.
	stub := `cd() {
  printf '%s\n' "$CSX_IDENTITY_CANARY" >&2
  if [ "$CSX_IDENTITY_FAIL" = deploy-directory ]; then return 4; fi
  [ "$#" -eq 1 ] && [ "$1" = /opt/codesamplex/deploy ]
}
docker() {
  cat >/dev/null
  printf '%s\n' "$CSX_IDENTITY_CANARY" >&2
  case "$*" in
    "inspect codesamplex-server-1 --format {{range .Config.Env}}"*) probe=container-revision ;;
    "inspect codesamplex-server-1 --format {{.Image}}") probe=container-image ;;
    "image inspect $CSX_IDENTITY_DIGEST --format "*) probe=image-revision ;;
    "compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/healthz") probe=health ;;
    "compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/version") probe=served-revision ;;
    *) return 99 ;;
  esac
  if [ "$CSX_IDENTITY_FAIL" = "$probe" ] && [ "$CSX_IDENTITY_VALID_FAILURE" != 1 ]; then
    printf '%s\n' "$CSX_IDENTITY_CANARY"
    return 4
  fi
  case "$probe" in
    container-revision) printf 'UNRELATED_SECRET=%s\nCSX_VERSION=%s\n' "$CSX_IDENTITY_CANARY" "$CSX_IDENTITY_SHA" ;;
    container-image) printf '%s\n' "$CSX_IDENTITY_DIGEST" ;;
    image-revision) printf '%s\n' "$CSX_IDENTITY_SHA" ;;
    health) printf 'ok\n' ;;
    served-revision) printf '{"revision":"%s"}\n' "$CSX_IDENTITY_SHA" ;;
  esac
  if [ "$CSX_IDENTITY_FAIL" = "$probe" ]; then return 4; fi
  return 0
}
`
	type collectorCase struct {
		name, fail string
		valid      bool
	}
	cases := []collectorCase{{name: "exact healthy evidence"}}
	for _, stage := range identityTestStages {
		cases = append(cases, collectorCase{name: stage + " exit 4", fail: stage})
	}
	// A successful sed/head must not hide a failed Docker/wget producer even
	// when that producer already printed a syntactically valid revision.
	for _, stage := range []string{"container-revision", "served-revision"} {
		cases = append(cases, collectorCase{name: stage + " valid output then exit 4", fail: stage, valid: true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, sh, "-c", remote)
			valid := "0"
			if tc.valid {
				valid = "1"
			}
			cmd.Env = append(os.Environ(), "CSX_IDENTITY_CANARY="+identityTestCanary, "CSX_IDENTITY_SHA="+identityTestSHA,
				"CSX_IDENTITY_DIGEST="+identityTestDigest, "CSX_IDENTITY_FAIL="+tc.fail, "CSX_IDENTITY_VALID_FAILURE="+valid)
			cmd.Stdin = strings.NewReader(prefix + stub + collector + suffix)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if tc.fail == "" {
				if err != nil || stdout.String() != identityTestEvidence() {
					t.Fatalf("healthy evidence changed: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
			} else {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 4 || stdout.Len() != 0 {
					t.Fatalf("failed probe was not closed: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
			}
			var markers strings.Builder
			for _, stage := range identityTestStages {
				markers.WriteString("CSX-IDENTITY-STAGE-V1 " + stage + "\n")
				if stage == tc.fail {
					break
				}
			}
			if stderr.String() != markers.String() {
				t.Fatalf("diagnostics must contain only completed/current fixed stages: %q", stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), identityTestCanary) {
				t.Fatal("collector exposed a synthetic secret")
			}
		})
	}
}

type identitySSHFixture struct {
	Name      string `json:"name"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exitCode"`
	WantError string `json:"wantError"`
}

// Read-ProductionState passes SSH flags directly to a native executable.
// Dispatch this test binary before Go flag parsing only in that local child;
// the production function itself remains unmodified and no SSH is executed.
func init() {
	if os.Getenv("CSX_TEST_IDENTITY_SSH_HELPER") != "1" || len(os.Args) < 2 || os.Args[1] != "-i" {
		return
	}
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(97)
	}
	collector, err := os.ReadFile(os.Getenv("CSX_TEST_IDENTITY_COLLECTOR"))
	if err != nil || string(payload) != "CSX-IDENTITY-V1\n{\n"+string(collector)+"\n} </dev/null\n" {
		os.Exit(98)
	}
	data, err := os.ReadFile(os.Getenv("CSX_TEST_IDENTITY_FIXTURES"))
	var fixtures []identitySSHFixture
	if err != nil || json.Unmarshal(data, &fixtures) != nil {
		os.Exit(97)
	}
	for _, fixture := range fixtures {
		if fixture.Name == os.Getenv("CSX_TEST_IDENTITY_CASE") {
			_, _ = io.WriteString(os.Stdout, fixture.Stdout)
			_, _ = io.WriteString(os.Stderr, fixture.Stderr)
			os.Exit(fixture.ExitCode)
		}
	}
	os.Exit(97)
}

func TestDeployIdentityWrapperSanitizesFailuresAndValidatesEvidence(t *testing.T) {
	pwsh := os.Getenv("CSX_TEST_PWSH")
	if pwsh == "" {
		pwsh, _ = exec.LookPath("pwsh")
	}
	if pwsh == "" {
		t.Skip("PowerShell 7 unavailable; set CSX_TEST_PWSH to its executable")
	}
	fixtures := []identitySSHFixture{{Name: "exact healthy evidence", Stdout: identityTestEvidence(), Stderr: identityTestCanary}}
	failed := func(name, stderr, stage string) {
		fixtures = append(fixtures, identitySSHFixture{Name: name, Stdout: identityTestEvidence() + identityTestCanary,
			Stderr: stderr, ExitCode: 4, WantError: "production identity probe failed (4); stage=" + stage})
	}
	for _, stage := range identityTestStages {
		failed(stage, identityTestCanary+"\nCSX-IDENTITY-STAGE-V1 "+stage+"\n"+identityTestCanary, stage)
	}
	failed("latest valid marker", "CSX-IDENTITY-STAGE-V1 container-image\r\nCSX-IDENTITY-STAGE-V1 health\r\n"+identityTestCanary, "health")
	failed("missing marker", identityTestCanary, "unavailable")
	failed("unknown stage", "CSX-IDENTITY-STAGE-V1 "+identityTestCanary+"\n", "unavailable")
	failed("prefix injection", identityTestCanary+" CSX-IDENTITY-STAGE-V1 health\n", "unavailable")
	failed("suffix injection", "CSX-IDENTITY-STAGE-V1 health "+identityTestCanary+"\n", "unavailable")
	failed("unknown after valid", "CSX-IDENTITY-STAGE-V1 health\nCSX-IDENTITY-STAGE-V1 "+identityTestCanary+"\n", "health")
	failed("large stderr", strings.Repeat(identityTestCanary+"\n", 8192)+"CSX-IDENTITY-STAGE-V1 served-revision\n", "served-revision")
	failed("stdout cannot select stage", identityTestCanary, "unavailable")
	fixtures[len(fixtures)-1].Stdout += "\nCSX-IDENTITY-STAGE-V1 health\n"
	for _, field := range []string{"revision", "image_revision", "served_revision"} {
		fixtures = append(fixtures, identitySSHFixture{Name: "malformed " + field,
			Stdout:    strings.Replace(identityTestEvidence(), field+"="+identityTestSHA+"\n", field+"="+identityTestCanary+"\n", 1),
			WantError: "production identity missing or malformed: " + field})
		fixtures = append(fixtures, identitySSHFixture{Name: "missing " + field,
			Stdout:    strings.Replace(identityTestEvidence(), field+"="+identityTestSHA+"\n", "", 1),
			WantError: "production identity missing or malformed: " + field})
	}
	for _, tc := range []struct{ name, from, to string }{
		{"malformed image", identityTestDigest, "sha256:short"},
		{"unhealthy", "health=ok", "health=unhealthy"},
	} {
		fixtures = append(fixtures, identitySSHFixture{Name: tc.name, Stdout: strings.Replace(identityTestEvidence(), tc.from, tc.to, 1),
			WantError: "production identity or health is invalid"})
	}
	fixtures = append(fixtures,
		identitySSHFixture{Name: "duplicate field", Stdout: identityTestEvidence() + "health=ok\n", WantError: "malformed production identity evidence"},
		identitySSHFixture{Name: "non evidence stdout", Stdout: identityTestEvidence() + identityTestCanary[:strings.Index(identityTestCanary, "=")], WantError: "malformed production identity evidence"})

	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "responses.json")
	data, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixturePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	program := `param([string]$Wrapper, [string]$CollectorPath, [string]$Helper, [string]$Fixtures)
$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'PowerShell 7 required' }
$tokens = $null; $parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($Wrapper, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw 'wrapper failed PowerShell parsing' }
$function = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Read-ProductionState' }, $true)
if ($null -eq $function) { throw 'identity function missing' }
. ([ScriptBlock]::Create($function.Extent.Text))
$collector = $CollectorPath
$ssh = $Helper
$KeyPath = 'fixture-key'; $KnownHostsPath = 'fixture-known-hosts'
$User = 'identity-fixture'; $Ip = '127.0.0.1'
$cases = Get-Content -Raw -LiteralPath $Fixtures | ConvertFrom-Json
foreach ($case in $cases) {
  $env:CSX_TEST_IDENTITY_CASE = $case.name
  $failure = ''; $state = $null
  try { $state = Read-ProductionState } catch { $failure = $_.Exception.Message }
  if ($failure -cne $case.wantError) { throw "case=$($case.name) unexpected result: $failure" }
  if ($case.wantError -eq '') {
    if ($state.Count -ne 5 -or $state.revision -cne ('a' * 40) -or $state.image_revision -cne ('a' * 40) -or
        $state.served_revision -cne ('a' * 40) -or $state.image_digest -cne ('sha256:' + ('b' * 64)) -or $state.health -cne 'ok') {
      throw 'exact healthy identity was changed'
    }
  }
  Write-Output "PASS identity case=$($case.name)"
}
# The production caller must still compare every identity against the exact
# expected rollback SHA after the collector validates the evidence format.
$guard = $ast.Find({ param($node) $node -is [Management.Automation.Language.IfStatementAst] -and $node.Extent.Text.Contains('production drifted from the expected exact rollback SHA') }, $true)
if ($null -eq $guard) { throw 'exact rollback SHA guard missing' }
$condition = [ScriptBlock]::Create($guard.Clauses[0].Item1.Extent.Text)
$ExpectedPreviousRevision = 'a' * 40
$before = @{ revision = $ExpectedPreviousRevision; image_revision = $ExpectedPreviousRevision; served_revision = $ExpectedPreviousRevision }
if (& $condition) { throw 'exact rollback identity rejected' }
foreach ($field in @('revision', 'image_revision', 'served_revision')) {
  $before[$field] = 'c' * 40
  if (-not (& $condition)) { throw "wrong rollback SHA accepted for $field" }
  $before[$field] = $ExpectedPreviousRevision
}
Write-Output 'PASS exact rollback SHA guard'
`
	scriptPath := filepath.Join(dir, "verify-identity.ps1")
	if err := os.WriteFile(scriptPath, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	wrapper, err := filepath.Abs("deploy-production.ps1")
	if err != nil {
		t.Fatal(err)
	}
	collector, err := filepath.Abs("collect-deploy-identity.sh")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", scriptPath, wrapper, collector, helper, fixturePath)
	cmd.Env = append(os.Environ(), "CSX_TEST_IDENTITY_SSH_HELPER=1", "CSX_TEST_IDENTITY_COLLECTOR="+collector, "CSX_TEST_IDENTITY_FIXTURES="+fixturePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("production identity wrapper fixture: err=%v\n%s", err, out)
	}
	if strings.Contains(string(out), identityTestCanary) {
		t.Fatal("wrapper printed a synthetic secret")
	}
	if strings.Count(string(out), "PASS identity case=") != len(fixtures) || !strings.Contains(string(out), "PASS exact rollback SHA guard") {
		t.Fatalf("wrapper fixtures did not all complete: %s", out)
	}
}
