package lightsail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivacySafeAccessLogDeploymentBoundary(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")
	caddy := readDeployFixture(t, filepath.Join("..", "caddy", "Caddyfile"))
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))

	copyCandidate := `Copy-Remote (Join-Path $repo "deploy\caddy\Caddyfile") "/opt/codesamplex/deploy/caddy/Caddyfile.candidate"`
	promote := `mv -f "$candidate" "$live"`
	recreate := `docker compose up -d --no-build --force-recreate caddy`
	reload := `docker compose exec -T caddy caddy reload`
	smoke := `$safeAccessLogSmoke = @'`
	positions := []int{
		strings.Index(script, copyCandidate),
		strings.Index(script, promote),
		strings.Index(script, recreate),
		strings.Index(script, reload),
		strings.Index(script, smoke),
	}
	lockAcquire := strings.Index(script, "Invoke-Remote $acquireDeployLock")
	lockRelease := strings.Index(script, "Invoke-Remote $releaseDeployLock")
	parentInstall := strings.Index(script, `Invoke-Remote "sudo install -d -o $User -g $User /opt/codesamplex"`)
	imageBuild := strings.Index(script, "& docker build --platform linux/amd64")
	imageSave := strings.Index(script, "& docker save $localImageTag -o $imageTar")
	if parentInstall < 0 || lockAcquire <= parentInstall || imageBuild <= lockAcquire || imageSave <= imageBuild || positions[0] <= imageSave || lockRelease <= positions[len(positions)-1] {
		t.Fatalf("deploy lock/bootstrap order is unsafe: parent=%d acquire=%d build=%d save=%d stages=%v release=%d", parentInstall, lockAcquire, imageBuild, imageSave, positions, lockRelease)
	}
	for i, position := range positions {
		if position < 0 {
			t.Fatalf("deployment invariant %d is missing", i)
		}
		if i > 0 && position <= positions[i-1] {
			t.Fatalf("deployment order = %v, want candidate copy < atomic promote < Caddy recreate < reload < live smoke", positions)
		}
	}
	for _, required := range []string{
		`-v "$candidate":/etc/caddy/Caddyfile:ro`,
		`if [ "$passed" -eq 0 ]; then rm -f "$candidate"; fi`,
		`sudo install -d -o $User -g $User /opt/codesamplex`,
		`[string]::Equals($Domain, "codesamplex.dev", [StringComparison]::OrdinalIgnoreCase)`,
		`$safeAccessLogSmoke.Replace('__CSX_DOMAIN__', $Domain)`,
		`Caddyfile.rollback-predeploy`,
		`docker compose up -d --no-build --no-deps --force-recreate caddy`,
		`test "$(cat "$lock/owner")" = "$owner"`,
		`test "$(find "$lock" -mindepth 1 -maxdepth 1 | wc -l)" -eq 1`,
		`rmdir "$lock"`,
		`csx-server-image-$deployLockOwner.tar`,
		`Remove-Item -LiteralPath $imageTar -Force`,
		`test "$(readlink -f "$old_dir")" = /var/log/caddy`,
		`[ ! -L "$old" ] || exit 66`,
		`"$old_dir"/access.log "$old_dir"/access-*.log "$old_dir"/access-*.log.gz`,
		`caddy:2.11.4-alpine`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("deploy.ps1 is missing %q", required)
		}
	}
	if strings.Contains(script, `Copy-Remote (Join-Path $repo "deploy\caddy\Caddyfile") "/opt/codesamplex/deploy/caddy/Caddyfile"`) {
		t.Fatal("candidate Caddyfile is copied over the live bind path before validation")
	}
	if strings.Contains(script, "rm -rf /var/log/caddy") || strings.Contains(script, "rm -rf /opt/codesamplex/.deploy-lock") || strings.Contains(script, "docker volume rm") {
		t.Fatal("legacy-log cleanup widened beyond exact access-log files")
	}
	if purge := strings.Index(script, "$legacyAccessPurge = @'"); purge <= positions[len(positions)-1] {
		t.Fatal("legacy query-bearing logs are purged before the replacement safe-log smoke succeeds")
	}

	for _, required := range []string{
		"output file /var/log/caddy-safe/access-safe.log",
		"roll_interval 24h",
		"roll_at 00:00",
		"request delete",
		"log_append @methodGetHead csx_method get_head",
		"log_append @methodPost csx_method post",
		"log_append @methodOther csx_method other",
		"log_skip @skipAPIMetrics",
	} {
		if !strings.Contains(caddy, required) {
			t.Errorf("Caddy privacy boundary is missing %q", required)
		}
	}
	for _, route := range []string{"search", "evidence", "registry", "shards", "samples", "wanted", "adoption", "verifications", "verification_jobs", "peers", "stats", "adapters", "auth"} {
		if !strings.Contains(caddy, "csx_route "+route) {
			t.Errorf("fixed Caddy route label %q is missing", route)
		}
	}
	if strings.Contains(caddy, "request>uri regexp") {
		t.Fatal("raw URI truncation is vulnerable to encoded path separators; URI must be deleted")
	}
	if !strings.Contains(compose, "image: caddy:2.11.4-alpine") {
		t.Fatal("compose must pin the exact Caddy release that supports time-based rolling")
	}

	serverStart := strings.Index(compose, "\n  server:\n")
	dbStart := strings.Index(compose, "\n  db:\n")
	if serverStart < 0 || dbStart <= serverStart {
		t.Fatal("could not isolate server service in docker-compose.yml")
	}
	server := compose[serverStart:dbStart]
	if !strings.Contains(server, "caddy_safe_logs:/var/log/caddy-safe:ro") {
		t.Fatal("server lacks the read-only dedicated safe-log mount")
	}
	if strings.Contains(server, "caddy_logs:/var/log/caddy") {
		t.Fatal("server can see the historical query-bearing Caddy volume")
	}
}

func readDeployFixture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}
