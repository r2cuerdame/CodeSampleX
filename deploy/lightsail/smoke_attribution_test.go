package lightsail

import (
	"strings"
	"testing"
)

// R2C-159: three of four unattended production rollouts either failed on, or
// barely survived, the deploy's very first request through the proxy. The
// transcript said only `curl: (28) Operation timed out`, and identifying which
// of the three probes had stalled needed the box's own edge access log after
// the fact. A rollout that fails closed has to name the request it failed on.
func TestPrivacySafeLogProbeNamesThePathItFailedOn(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")

	smoke := strings.Index(script, "$safeAccessLogSmoke = @'")
	if smoke < 0 {
		t.Fatal("the privacy-safe access log smoke is gone")
	}
	end := strings.Index(script[smoke:], "\n'@")
	if end < 0 {
		t.Fatal("the privacy-safe access log smoke is unterminated")
	}
	body := script[smoke : smoke+end]

	for _, required := range []string{
		// One helper, so the ceiling and the reporting cannot drift apart
		// between the three probes.
		`log_probe() {`,
		`probe_code=$?`,
		`echo "FAIL privacy-safe log probe $probe_path: curl exit $probe_code" >&2`,
		// It stays fail-closed: naming the failure must not swallow it.
		`exit 1`,
		`log_probe '/v1/stats?csx_safe_log_smoke=discard-this-query'`,
		`log_probe '/v1/samples%2Fencoded-marker-must-not-log/path'`,
		`log_probe '/v1/secret-marker-must-not-log/path'`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("privacy-safe log probe is missing %q", required)
		}
	}

	// Every probe goes through the helper. A bare curl here is a request that
	// can time out anonymously again.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "curl ") && !strings.Contains(trimmed, `"https://__CSX_DOMAIN__$probe_path"`) {
			t.Errorf("unattributed probe request in the safe-log smoke: %q", trimmed)
		}
	}

	// The probe's ceiling is what turned a slow endpoint into a failed
	// rollout, so it stays visible and in one place.
	if strings.Count(body, "--max-time 10") != 1 {
		t.Errorf("safe-log probe ceilings = %d occurrences, want exactly one shared ceiling",
			strings.Count(body, "--max-time 10"))
	}
}
