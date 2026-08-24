package lightsail

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	lockAcquire := strings.Index(script, "Invoke-RemoteScript $acquireDeployLock")
	lockRelease := strings.Index(script, "Invoke-RemoteScript $releaseDeployLock")
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
		`function Invoke-RemoteScript([string]$Script)`,
		`$process.StandardInput.BaseStream.Write($scriptBytes, 0, $scriptBytes.Length)`,
		`$psi.Arguments = '-i "' + $resolvedKeyPath + '" -o StrictHostKeyChecking=yes -o UserKnownHostsFile="' + $resolvedKnownHostsPath + '" -o ConnectTimeout=20 ' + $remote + ' "sh -s"'`,
		`Invoke-RemoteScript $caddyConfigPreflight`,
		`Invoke-RemoteScript $promoteCaddy`,
		`Invoke-RemoteScript $safeAccessLogSmoke`,
		`Invoke-RemoteScript $rollbackCaddy`,
		`Invoke-RemoteScript $legacyAccessPurge`,
		`Invoke-RemoteScript $adminProbe`,
		`Invoke-RemoteScript $releaseDeployLock`,
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
	for _, unsafe := range []string{
		`Invoke-Remote $acquireDeployLock`,
		`Invoke-Remote $caddyConfigPreflight`,
		`Invoke-Remote $promoteCaddy`,
		`Invoke-Remote $safeAccessLogSmoke`,
		`Invoke-Remote $rollbackCaddy`,
		`Invoke-Remote $legacyAccessPurge`,
		`Invoke-Remote $adminProbe`,
		`Invoke-Remote $releaseDeployLock`,
	} {
		if strings.Contains(script, unsafe) {
			t.Errorf("quote-sensitive multiline program still crosses argv: %q", unsafe)
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
	for _, unsupported := range []string{"network's whole posture", "server stores no identifiers", "CodeSampleX stores no identifiers"} {
		if strings.Contains(caddy, unsupported) {
			t.Fatalf("Caddy collector comment makes unsupported server-wide privacy claim %q", unsupported)
		}
	}
	if !strings.Contains(caddy, "deliberately limited to this collector") {
		t.Fatal("Caddy privacy copy does not scope no-IP storage to the safe API activity/access-log collector")
	}
	for _, disclosure := range []string{"activity_buckets", "CSX_ACTIVITY_HASH_KEY", "IPv4 buckets are enumerable", "colocated key"} {
		if !strings.Contains(caddy, disclosure) {
			t.Errorf("Caddy activity pseudonym disclosure missing %q", disclosure)
		}
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
	if !strings.Contains(server, "CSX_ACTIVITY_HASH_KEY: ${CSX_ACTIVITY_HASH_KEY:-}") {
		t.Fatal("server does not receive the dedicated activity hash key")
	}
	for _, required := range []string{
		`$ensureActivityKey = @'`,
		`key=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')`,
		`grep -Eq '^CSX_ACTIVITY_HASH_KEY=[0-9a-f]{64}$' .env`,
		`chmod 0600 .env`,
		`if [ -s "$tmp" ] && [ -n "$(tail -c 1 "$tmp")" ]; then printf '\n' >> "$tmp"; fi`,
		`[IO.File]::WriteAllText($envTmp, $normalizedEnvText, [Text.Encoding]::ASCII)`,
		`Invoke-RemoteScript $ensureActivityKey`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("activity key deployment missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"CSX_ACTIVITY_HASH_KEY=$pw", "Invoke-Remote $ensureActivityKey", "Set-Content -Path $envTmp -Value $key",
		// The key must never be echoed, logged, or returned over SSH: the
		// deploy only ever reports that one is present.
		`echo "$key"`, `echo $key`, `printf '%s\n' "$key"`, "Write-Output $key",
		`Write-Output "$key"`, `Write-Host $key`,
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("activity key crosses an unsafe boundary: %q", forbidden)
		}
	}
	// The one place the key is read back must consume it inside a quiet
	// matcher rather than printing it across the SSH channel.
	if !strings.Contains(script, `printf "%s\n" "$CSX_ACTIVITY_HASH_KEY" | grep -Eq "^[0-9a-f]{64}$"`) {
		t.Error("activity key smoke check no longer verifies the key without emitting it")
	}
	keyInstall := strings.Index(script, "Invoke-RemoteScript $ensureActivityKey")
	serverStart = strings.Index(script, `docker compose up -d --no-build --force-recreate server`)
	if keyInstall < 0 || serverStart <= keyInstall {
		t.Fatal("activity key is not installed before rolling server recreation")
	}
}

func TestActivityKeyInstallIsAtomicAcrossFreshUpgradeAndRerun(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	script := readDeployFixture(t, "deploy.ps1")
	startMarker := "$ensureActivityKey = @'\n"
	start := strings.Index(script, startMarker)
	if start < 0 {
		t.Fatal("activity-key here-string missing")
	}
	start += len(startMarker)
	end := strings.Index(script[start:], "\n'@")
	if end < 0 {
		t.Fatal("activity-key here-string terminator missing")
	}
	program := script[start : start+end]
	program = strings.Replace(program, "cd /opt/codesamplex/deploy", `cd "$CSX_TEST_DEPLOY_DIR"`, 1)
	keyLine := regexp.MustCompile(`^CSX_ACTIVITY_HASH_KEY=([0-9a-f]{64})$`)

	for _, tc := range []struct {
		name    string
		initial string
	}{
		{"fresh-host-no-final-newline", "POSTGRES_PASSWORD=fresh-password"},
		{"upgrade-final-newline", "POSTGRES_PASSWORD=upgrade-password\nCSX_PUBLIC_URL=https://example.test\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			envPath := filepath.Join(dir, ".env")
			if err := os.WriteFile(envPath, []byte(tc.initial), 0o600); err != nil {
				t.Fatal(err)
			}
			run := func() []byte {
				cmd := exec.Command(sh, "-c", program)
				cmd.Env = append(os.Environ(), "CSX_TEST_DEPLOY_DIR="+filepath.ToSlash(dir))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("key install failed: %v: %s", err, out)
				}
				if len(out) != 0 {
					t.Fatalf("key install emitted secret-bearing output: %q", out)
				}
				raw, err := os.ReadFile(envPath)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}

			first := run()
			if !bytes.HasSuffix(first, []byte("\n")) {
				t.Fatal("installed env is not newline terminated")
			}
			lines := strings.Split(strings.TrimSuffix(string(first), "\n"), "\n")
			var key string
			keyCount := 0
			for _, line := range lines {
				if match := keyLine.FindStringSubmatch(line); match != nil {
					key, keyCount = match[1], keyCount+1
				}
			}
			if keyCount != 1 {
				t.Fatalf("activity key lines = %d in %q", keyCount, first)
			}
			if !strings.Contains(string(first), strings.Split(tc.initial, "\n")[0]+"\n") {
				t.Fatalf("password line changed or absorbed the key: %q", first)
			}
			if passwordLine := lines[0]; strings.Contains(passwordLine, key) {
				t.Fatal("activity key was embedded in POSTGRES_PASSWORD/DSN input")
			}

			second := run()
			if !bytes.Equal(first, second) {
				t.Fatal("rerun changed the stable key or env bytes")
			}
			matches, err := filepath.Glob(filepath.Join(dir, ".env.activity.*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("atomic installer left temporary files: %v err=%v", matches, err)
			}
		})
	}
}

func TestServerRolloutHasExactIndependentRollbackThroughActivitySmoke(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")

	stages := []string{
		`$snapshotServerConfig = @'`,
		`docker tag "$old" codesamplex/csx-server:rollback-predeploy`,
		`Invoke-RemoteScript $snapshotServerConfig`,
		`Copy-Remote (Join-Path $repo "deploy\docker-compose.yml") "/opt/codesamplex/deploy/docker-compose.yml.candidate"`,
		`Invoke-RemoteScript $promoteServerConfig`,
		`$serverActivationStarted = $true`,
		`docker compose up -d --no-build --force-recreate server`,
		`throw "healthz never returned ok"`,
		`Invoke-RemoteScript $adminProbe`,
		`Invoke-RemoteScript $activitySmoke`,
		`throw "served SHA does not match the immutable deployment revision"`,
		`throw "complete + partial + missing + legacy-evidence-incomplete does not equal FAIL"`,
		`landing sample:`,
		`Invoke-RemoteScript $legacyAccessPurge`,
		`Invoke-RemoteScript $commitDeployment`,
	}
	last := -1
	for _, stage := range stages {
		position := strings.Index(script, stage)
		if position < 0 {
			t.Fatalf("server rollout/rollback contract is missing %q", stage)
		}
		if position <= last {
			t.Fatalf("server rollout stage %q is out of order", stage)
		}
		last = position
	}

	commit := strings.Index(script, `Invoke-RemoteScript $commitDeployment`)
	rollback := strings.Index(script, `$rollbackServer = @'`)
	rollbackCaddy := strings.Index(script, `$rollbackCaddy = @'`)
	aggregate := strings.Index(script[rollbackCaddy:], `throw [AggregateException]::new`)
	if commit < 0 || rollback <= commit || rollbackCaddy <= rollback || aggregate < 0 {
		t.Fatalf("rollback lifetime/order is unsafe: commit=%d server=%d caddy=%d aggregate=%d", commit, rollback, rollbackCaddy, aggregate)
	}

	for _, required := range []string{
		`server-container.rollback-present`,
		`server-container.rollback-absent`,
		`server-container.rollback-running`,
		`server-container.rollback-stopped`,
		`server-latest.rollback-id`,
		`server-latest.rollback-absent`,
		`test "$(docker image inspect codesamplex/csx-server:rollback-predeploy --format '{{.Id}}')" = "$old"`,
		`cp -p docker-compose.yml.rollback-predeploy docker-compose.yml`,
		`rm -f docker-compose.yml`,
		`cp -p .env.rollback-predeploy .env`,
		`rm -f .env`,
		`rm -f docker-compose.yml.candidate .env.new .env.activity.* .env.admin.* caddy/Caddyfile.candidate`,
		`docker tag codesamplex/csx-server:rollback-predeploy codesamplex/csx-server:latest`,
		`docker image rm codesamplex/csx-server:latest`,
		`docker compose up -d --no-build --no-deps --force-recreate server`,
		`docker compose exec -T server wget -qO- http://127.0.0.1:8080/healthz`,
		`test "$(docker inspect codesamplex-server-1 --format '{{.Image}}')" = "$old"`,
		`cmp -s docker-compose.yml.rollback-predeploy docker-compose.yml`,
		`cmp -s .env.rollback-predeploy .env`,
		`! docker container inspect codesamplex-server-1`,
		`! docker image inspect codesamplex/csx-server:latest`,
		`$serverRollbackFailure = $_`,
		`$caddyRollbackFailure = $_`,
		`$credentialRollbackFailure = $_`,
		`$allFailures.Add($deployFailure.Exception)`,
		`throw $deployFailure`,
		`SELECT to_regclass('public.activity_buckets') IS NOT NULL`,
		`SELECT to_regclass('public.activity_health') IS NOT NULL`,
		`test "$columns" = kind,epoch,bucket,owner,first_seen,last_seen`,
		`test "$owner_epochs" = 2`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("server rollback/activity smoke is missing %q", required)
		}
	}
	if strings.Contains(script, `Write-Warning "server rollout failed`) || strings.Contains(script, `Write-Warning "Caddy activation failed`) {
		t.Fatal("rollback failure is merely warned instead of preserved with the deployment failure")
	}
	if strings.Contains(script, `Copy-Remote (Join-Path $repo "deploy\docker-compose.yml") "/opt/codesamplex/deploy/docker-compose.yml"`) {
		t.Fatal("new compose config overwrites the live config before the rollback snapshot and activation transaction")
	}
}

func TestAutomaticDeployNeverPerformsTheIrreversibleLegacyLogPurge(t *testing.T) {
	deploy := readDeployFixture(t, "deploy.ps1")
	wrapper := readDeployFixture(t, "deploy-production.ps1")
	for _, required := range []string{
		`[switch]$RequireNoLegacyAccessLogs`,
		`legacy query-bearing access log requires a manual privacy cleanup`,
		`automatic deploy performed no irreversible legacy-log cleanup`,
	} {
		if !strings.Contains(deploy, required) {
			t.Errorf("automatic deployment privacy gate is missing %q", required)
		}
	}
	if !strings.Contains(wrapper, `-RequireNoLegacyAccessLogs`) {
		t.Fatal("the production Actions wrapper can still execute the irreversible legacy-log purge")
	}
}

func TestAdminCredentialCommitFollowsEverySmokeAndRemoteCommit(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")
	commitLocal := strings.Index(script, `Commit-CSXAdminCredential $adminCredentialPaths.Pending $adminCredentialPaths.Active`)
	if commitLocal < 0 {
		t.Fatal("pending DPAPI credential is never committed")
	}
	for _, prior := range []string{
		`Write-Output "healthz: ok"`,
		`Invoke-RemoteScript $safeAccessLogSmoke`,
		`Invoke-RemoteScript $adminProbe`,
		`admin authenticated smoke: 200`,
		`Invoke-RemoteScript $activitySmoke`,
		`landing sample:`,
		`Invoke-RemoteScript $legacyAccessPurge`,
		`Invoke-RemoteScript $commitDeployment`,
	} {
		position := strings.Index(script, prior)
		if position < 0 || position >= commitLocal {
			t.Errorf("local credential committed before required stage %q (stage=%d commit=%d)", prior, position, commitLocal)
		}
	}
	for _, required := range []string{
		`ActiveExisted  = $activeExisted`,
		`PendingExisted = $pendingExisted`,
		`function Restore-CSXAdminCredentialRelationship`,
		`Restore-CSXAdminCredentialRelationship $adminCredentialPaths $adminCredentialState`,
		`local admin credential active/pending relationship was not restored`,
		`Get-FileHash -LiteralPath $Paths.Active`,
		`Get-FileHash -LiteralPath $Paths.Pending`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("credential transaction is missing %q", required)
		}
	}
	restore := strings.Index(script, `Restore-CSXAdminCredentialRelationship $adminCredentialPaths $adminCredentialState`)
	remoteEnvRestore := strings.Index(script, `cp -p .env.rollback-predeploy .env`)
	if restore <= remoteEnvRestore || restore <= commitLocal {
		t.Fatalf("credential/remote rollback order does not cover a late local commit failure: remote=%d commit=%d local-restore=%d", remoteEnvRestore, commitLocal, restore)
	}
}

func readDeployFixture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRemoteShellSensitiveScriptsAreLFPinnedAndStored(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	list := exec.Command("git", "-C", repoRoot, "ls-files", "-z", "--", "*.ps1", "*.sh")
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files: %v\n%s", err, out)
	}
	trimmed := bytes.TrimSuffix(out, []byte{0})
	if len(trimmed) == 0 {
		t.Fatal("git found no PowerShell or shell scripts to validate")
	}
	files := strings.Split(string(trimmed), "\x00")

	args := []string{"-C", repoRoot, "check-attr", "eol", "--"}
	args = append(args, files...)
	check := exec.Command("git", args...)
	attrs, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("git check-attr: %v\n%s", err, attrs)
	}
	attrLines := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(attrs)), "\n") {
		attrLines[strings.TrimSpace(line)] = true
	}

	seenPS, seenSH := false, false
	for _, name := range files {
		name = filepath.ToSlash(name)
		seenPS = seenPS || strings.HasSuffix(name, ".ps1")
		seenSH = seenSH || strings.HasSuffix(name, ".sh")
		if !attrLines[name+": eol: lf"] {
			t.Errorf("%s is not pinned to eol=lf; git check-attr output:\n%s", name, attrs)
		}
		raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(name)))
		if err != nil {
			t.Error(err)
			continue
		}
		if offset := bytes.IndexByte(raw, '\r'); offset >= 0 {
			t.Errorf("%s contains CR byte at offset %d; remote sh input must be LF-only", name, offset)
		}
	}
	if !seenPS || !seenSH {
		t.Fatalf("script inventory missing a required family: ps1=%v sh=%v", seenPS, seenSH)
	}
}

// The activity trust boundary treats a loopback or private-range peer as the
// reverse proxy and takes its X-Forwarded-For at face value. That is only
// sound while nothing else can reach the server's listener, so the deployment
// must never publish the server port to the host or the internet: Caddy is the
// single ingress, and the server is reachable only on the compose network.
func TestServerListenerIsNeverPublishedOutsideTheComposeNetwork(t *testing.T) {
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))

	// Guard the extractor itself: if a block ever bled into its neighbour, the
	// "server has no ports:" assertion below would pass for the wrong reason.
	if s := composeService(t, compose, "caddy"); strings.Contains(s, "CSX_LISTEN") || strings.Contains(s, "POSTGRES_USER") {
		t.Fatal("compose block extraction leaked into a neighbouring service")
	}
	if s := composeService(t, compose, "server"); strings.Contains(s, "caddy_data") || strings.Contains(s, "POSTGRES_USER") {
		t.Fatal("compose block extraction leaked into a neighbouring service")
	}

	server := composeService(t, compose, "server")
	if !strings.Contains(server, "expose:") {
		t.Fatal("server service does not declare expose; the listener boundary is undocumented")
	}
	if strings.Contains(server, "ports:") {
		t.Fatal("server service publishes ports; a forged X-Forwarded-For could then reach the collector directly")
	}
	if strings.Contains(server, "network_mode: host") || strings.Contains(server, "network_mode: \"host\"") {
		t.Fatal("server service uses host networking, which exposes the listener directly")
	}
	// The listener must stay on the container-internal address family only —
	// binding it is fine, publishing it is not.
	if !strings.Contains(server, `CSX_LISTEN: ":8080"`) {
		t.Fatal("server listen address changed; re-verify the trust boundary")
	}

	db := composeService(t, compose, "db")
	if strings.Contains(db, "ports:") {
		t.Fatal("database publishes ports; activity buckets must not be reachable from outside the host stack")
	}

	caddy := composeService(t, compose, "caddy")
	for _, published := range []string{`"80:80"`, `"443:443"`} {
		if !strings.Contains(caddy, published) {
			t.Fatalf("caddy no longer publishes %s; ingress assumptions changed", published)
		}
	}
	if strings.Contains(caddy, "8080:") {
		t.Fatal("caddy publishes the server port directly, bypassing the proxy boundary")
	}
}

// composeService returns the YAML block of one service, so a `ports:` key
// belonging to a different service can never satisfy an assertion here.
func composeService(t *testing.T, compose, name string) string {
	t.Helper()
	start := strings.Index(compose, "\n  "+name+":\n")
	if start < 0 {
		t.Fatalf("compose service %q not found", name)
	}
	start++
	rest := compose[start+len("  "+name+":\n"):]
	for offset := 0; ; {
		newline := strings.IndexByte(rest[offset:], '\n')
		if newline < 0 {
			return rest
		}
		lineStart := offset + newline + 1
		line := rest[lineStart:]
		if end := strings.IndexByte(line, '\n'); end >= 0 {
			line = line[:end]
		}
		// A new top-level key or a sibling service ends this block.
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "  #") {
			if !strings.HasPrefix(line, "  ") || strings.HasSuffix(strings.TrimSpace(line), ":") {
				return rest[:lineStart]
			}
		}
		offset = lineStart
	}
}
