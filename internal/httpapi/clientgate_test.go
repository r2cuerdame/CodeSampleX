package httpapi

import (
	"net/http"
	"testing"
)

// A worker that cannot detect its own container platform must not be given
// work. Before v0.1.22 the client sent the literal "linux" whatever daemon it
// was talking to, so a Windows daemon produced receipts stamped linux — false
// evidence in a network whose product is that the environment recorded is the
// environment that ran. The server cannot tell such a worker apart from an
// honest one, so the only defence is to refuse anything that cannot say what
// it is.
func TestWorkRequestRefusesClientsThatCannotReportTheirPlatform(t *testing.T) {
	for _, tc := range []struct {
		version string
		allowed bool
		why     string
	}{
		{"v0.1.22", true, "the version that detects its container OS"},
		{"v0.1.23", true, "anything newer"},
		{"v0.2.0", true, "a later minor"},
		{"v0.1.21", false, "still hardcodes linux"},
		{"v0.1.20", false, "still hardcodes linux"},
		{"", false, "says nothing, so it cannot be trusted to say linux honestly"},
		{"dev (git)", false, "unstamped build; the network cannot tell what it does"},
		{"garbage", false, "unparseable"},
	} {
		err := checkAuthoringClient(tc.version)
		if tc.allowed && err != nil {
			t.Errorf("%q (%s) refused: %v", tc.version, tc.why, err)
		}
		if !tc.allowed && err == nil {
			t.Errorf("%q (%s) was allowed", tc.version, tc.why)
		}
	}
}

// The refusal has to tell the operator what to do, and say it with the status
// that means exactly this.
func TestClientGateRefusalIsActionable(t *testing.T) {
	err := checkAuthoringClient("v0.1.20")
	if err == nil {
		t.Fatal("an old client was allowed")
	}
	if got := err.Error(); got == "" || !containsAll(got, "csx update", minAuthoringClient) {
		t.Errorf("message %q does not name the fix and the required version", got)
	}
	if authoringClientGateStatus != http.StatusUpgradeRequired {
		t.Errorf("status = %d, want 426 Upgrade Required", authoringClientGateStatus)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		found := false
		for i := 0; i+len(p) <= len(s); i++ {
			if s[i:i+len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
