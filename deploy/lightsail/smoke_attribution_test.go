package lightsail

import (
	"os/exec"
	"strings"
	"testing"
)

func observationAttributionFunction(t *testing.T, name string) string {
	t.Helper()
	script := readDeployFixture(t, "collect-extended-observation.sh")
	start := strings.Index(script, name+"() {\n")
	if start < 0 {
		t.Fatalf("missing observation function %s", name)
	}
	body := script[start:]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("unterminated observation function %s", name)
	}
	return body[:end+3]
}

// Preserve the production timeout regression's attribution after moving the
// probes to observation. A failed request names only a fixed logical probe,
// never the unique marker, arbitrary URL, log line, or response body.
func TestPrivacyObservationNamesEveryFailedRequestSafely(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	program := `set -eu
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
DOMAIN=codesamplex.dev
curl() { return 28; }
docker() { printf unavailable; }
sleep() { :; }
` + observationAttributionFunction(t, "privacy_live") + "\nprivacy_live\n"
	out, err := exec.Command(sh, "-c", program).CombinedOutput()
	if err != nil {
		t.Fatalf("privacy observation failed: %v: %s", err, out)
	}
	for _, want := range []string{"probe_privacy_live_1_curl_exit=28", "probe_privacy_live_2_curl_exit=28", "probe_privacy_live_3_curl_exit=28", "unavailable"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing safe request attribution %q: %s", want, out)
		}
	}
	for _, unsafe := range []string{"csx-observe-secret-", "https://", "/v1/"} {
		if strings.Contains(string(out), unsafe) {
			t.Errorf("privacy diagnostics exposed arbitrary request data %q", out)
		}
	}
}

func TestObservationDiagnosticsExposeOnlyFixedNumericProbeFields(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	program := `set -eu
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fixture() {
  printf 'probe_privacy_live_1_curl_exit=28\n'
  printf 'private-body-do-not-emit\n'
  printf 'probe_bad_status=private-value\n'
  printf 'unavailable\n'
}
` + observationAttributionFunction(t, "run_check") + "\nrun_check privacy_live fixture\n"
	out, err := exec.Command(sh, "-c", program).CombinedOutput()
	if err != nil {
		t.Fatalf("diagnostic sanitizer failed: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "probe_privacy_live_1_curl_exit=28") || !strings.Contains(string(out), "privacy_live=unavailable") {
		t.Fatalf("safe diagnostic or classification lost: %s", out)
	}
	if strings.Contains(string(out), "private-") {
		t.Fatalf("arbitrary diagnostic data escaped: %s", out)
	}
}
