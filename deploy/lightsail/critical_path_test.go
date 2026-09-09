package lightsail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func deployHereString(t *testing.T, name string) string {
	t.Helper()
	script := readDeployFixture(t, "deploy.ps1")
	marker := "$" + name + " = @'\n"
	start := strings.Index(script, marker)
	if start < 0 {
		t.Fatalf("deploy script has no %s here-string", name)
	}
	body := script[start+len(marker):]
	end := strings.Index(body, "\n'@")
	if end < 0 {
		t.Fatalf("unterminated %s here-string", name)
	}
	return body[:end]
}

func TestRepresentativeRequestsRejectWrongContentAndExactSHA(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	program := strings.ReplaceAll(deployHereString(t, "representativeSmoke"), "__CSX_DOMAIN__", "codesamplex.dev")
	program = strings.ReplaceAll(program, "__CSX_REVISION__", revision)
	// Run the actual requests against a deterministic curl adapter. No request
	// reaches production; assertions concern the shipped shell verifier.
	stub := `curl() {
  body=''; last=''
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then shift; body="$1"; fi
    last="$1"; shift
  done
  case "$last" in
    */features)
      if [ "$CSX_CASE" = invalid-content ]; then printf 'unrelated page' > "$body"
      else printf '<link rel="canonical" href="https://codesamplex.dev/features">' > "$body"; fi ;;
    */version)
      if [ "$CSX_CASE" = wrong-sha ]; then printf '{"revision":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}' > "$body"
      else printf '{"revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' > "$body"; fi ;;
    *) echo "unexpected representative route $last" >&2; return 99 ;;
  esac
  if [ "$CSX_CASE" = transport ]; then return 28; fi
  if [ "$CSX_CASE" = unavailable ]; then printf 503; else printf 200; fi
}
`
	for _, name := range []string{"healthy", "invalid-content", "wrong-sha", "unavailable", "transport"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, sh, "-c", stub+program)
			cmd.Env = append(os.Environ(), "CSX_CASE="+name)
			out, err := cmd.CombinedOutput()
			if (err == nil) != (name == "healthy") {
				t.Fatalf("representative result = %v: %s", err, out)
			}
			if name == "healthy" && strings.Count(string(out), "ok representative") != 2 {
				t.Fatalf("expected exactly two requests: %s", out)
			}
		})
	}
}

func TestRollbackRestoresSnapshotAndRejectsWrongServedIdentity(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	program := strings.Replace(deployHereString(t, "rollbackServer"), "cd /opt/codesamplex/deploy", `cd "$CSX_TEST_DIR"`, 1)
	program = strings.ReplaceAll(program, "__CSX_RESTORE_DIST__", "0")
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stub := `docker() {
  case "$*" in
    "image inspect codesamplex/csx-server:rollback-predeploy"*) printf '%s\n' "$CSX_OLD_IMAGE" ;;
    "container inspect "*) test -f container-alive ;;
    "rm -f "*) rm -f container-alive ;;
    "tag "*) touch latest-alive ;;
    "compose up "*) touch container-alive ;;
    "inspect codesamplex-server-1 --format {{.Image}}") printf '%s\n' "$CSX_OLD_IMAGE" ;;
    "inspect codesamplex-server-1 --format {{.State.Running}}") printf 'true\n' ;;
    "inspect codesamplex-server-1 --format {{range .Config.Env}}"*) printf 'CSX_VERSION=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n' ;;
    "compose exec "*"/healthz") printf 'ok\n' ;;
    "compose exec "*"/version") printf '{"revision":"%s"}\n' "$CSX_SERVED_SHA" ;;
    "image inspect codesamplex/csx-server:latest"*) test -f latest-alive ;;
    "image rm codesamplex/csx-server:latest") rm -f latest-alive ;;
    *) echo "unexpected rollback command: $*" >&2; return 97 ;;
  esac
}
`
	for _, tc := range []struct {
		name, sha string
		corrupt   bool
	}{
		{"exact rollback", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{"wrong served SHA", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", false},
		{"ambiguous rollback snapshot", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"docker-compose.yml.rollback-predeploy": "old-compose\n", ".env.rollback-predeploy": "OLD=secret\n",
				"docker-compose.yml": "new-compose\n", ".env": "NEW=secret\n",
				"server-container.rollback-present": "", "server-container.rollback-running": "",
				"server-latest.rollback-absent": "", "server-image.rollback-id": digest + "\n", "container-alive": "",
			}
			if tc.corrupt {
				files["server-container.rollback-absent"] = ""
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, sh, "-c", stub+program)
			cmd.Env = append(os.Environ(), "CSX_TEST_DIR="+filepath.ToSlash(dir), "CSX_OLD_IMAGE="+digest, "CSX_SERVED_SHA="+tc.sha)
			out, err := cmd.CombinedOutput()
			if (err == nil) != (tc.name == "exact rollback") {
				t.Fatalf("rollback result = %v: %s", err, out)
			}
			if tc.corrupt {
				got, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
				if string(got) != "new-compose\n" {
					t.Fatal("ambiguous snapshot was applied instead of refusing mutation")
				}
			} else {
				for _, name := range []string{"docker-compose.yml", ".env"} {
					got, _ := os.ReadFile(filepath.Join(dir, name))
					want := files[name+".rollback-predeploy"]
					if string(got) != want {
						t.Fatalf("%s was not restored exactly", name)
					}
				}
			}
		})
	}
}

func TestDeploymentBudgetBoundsProcessesAndReservesRollback(t *testing.T) {
	pwsh := os.Getenv("CSX_TEST_PWSH")
	if pwsh == "" {
		pwsh, _ = exec.LookPath("pwsh")
	}
	if pwsh == "" {
		t.Skip("PowerShell 7 unavailable; set CSX_TEST_PWSH to its executable")
	}
	helper, err := filepath.Abs("deploy-budget.ps1")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	program := `param([string]$Helper)
$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'PowerShell 7 required' }
. $Helper
$exe = (Get-Process -Id $PID).Path
Set-DeployPhase normal 30
$result = @(Invoke-DeployProcess $exe @('-NoProfile','-Command',"Write-Output 'served'"))
if ($result.Count -ne 1 -or $result[0] -ne 'served') { throw 'stdout not preserved' }
$caught = $false
try { Invoke-DeployProcess $exe @('-NoProfile','-Command',"Write-Error 'native failure'; exit 42") | Out-Null } catch {
  if ($_.Exception.Message -notmatch 'command failed \(42\)') { throw }
  $caught = $true
}
if (-not $caught) { throw 'native error swallowed' }
$watch = [Diagnostics.Stopwatch]::StartNew()
$caught = $false
try { Invoke-DeployProcess $exe @('-NoProfile','-Command','Start-Sleep 20') 1 | Out-Null } catch {
  if ($_.Exception.Message -notmatch 'exceeded 1s') { throw }
  $caught = $true
}
if (-not $caught -or $watch.Elapsed.TotalSeconds -gt 7) { throw 'synchronous process timeout was unbounded' }
Set-DeployPhase exhausted 1
$caught = $false
try { Get-DeployBudgetSeconds | Out-Null } catch { $caught = $_.Exception.Message -match 'exhausted' }
if (-not $caught) { throw 'exhausted phase started new work' }
Set-DeployPhase rollback 20
$result = @(Invoke-DeployProcess $exe @('-NoProfile','-Command',"Write-Output 'rolled-back'"))
if ($result[0] -ne 'rolled-back') { throw 'rollback had no independent reserve' }
Write-Output 'PASS budget execution'
`
	path := filepath.Join(dir, "verify-budget.ps1")
	if err := os.WriteFile(path, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, pwsh, "-NoLogo", "-NoProfile", "-File", path, helper).CombinedOutput()
	if err != nil {
		t.Fatalf("budget execution failed: %v\n%s", err, out)
	}
	for _, required := range []string{"PASS budget execution", "phase=normal elapsed_seconds=", "phase=rollback", "ceiling_seconds="} {
		if !strings.Contains(string(out), required) {
			t.Errorf("budget evidence missing %q: %s", required, out)
		}
	}
}

func TestInitialSnapshotClearsStaleCaddyRecoveryState(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "caddy"), 0700); err != nil {
		t.Fatal(err)
	}
	stale := []string{"Caddyfile.rollback-predeploy", "Caddyfile.rollback-absent", "container.rollback-present", "container.rollback-absent", "container.rollback-running", "container.rollback-stopped", "container.rollback-image-id"}
	for _, name := range stale {
		if err := os.WriteFile(filepath.Join(dir, "caddy", name), []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "caddy", "Caddyfile"), []byte("current proxy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("current compose"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("CURRENT=value"), 0600); err != nil {
		t.Fatal(err)
	}
	program := strings.Replace(deployHereString(t, "snapshotServerConfig"), "cd /opt/codesamplex/deploy", `cd "$CSX_TEST_DIR"`, 1)
	// A first-deploy host has no server/image. Snapshot commands still must
	// clear old proxy markers before any later promotion can be attempted.
	cmd := exec.Command(sh, "-c", "docker() { return 1; }\n"+program)
	cmd.Env = append(os.Environ(), "CSX_TEST_DIR="+filepath.ToSlash(dir))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("snapshot failed: %v: %s", err, out)
	}
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(dir, "caddy", name)); !os.IsNotExist(err) {
			t.Errorf("stale recovery state survived: %s", name)
		}
	}
	live, _ := os.ReadFile(filepath.Join(dir, "caddy", "Caddyfile"))
	if string(live) != "current proxy" {
		t.Fatal("snapshot mutated the active proxy config")
	}
	for _, name := range []string{"docker-compose.yml", ".env"} {
		before, _ := os.ReadFile(filepath.Join(dir, name))
		saved, _ := os.ReadFile(filepath.Join(dir, name+".rollback-predeploy"))
		if string(before) != string(saved) {
			t.Errorf("%s snapshot is not exact", name)
		}
	}
}
