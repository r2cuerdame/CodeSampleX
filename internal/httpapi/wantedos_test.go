package httpapi

import (
	"testing"
	"time"
)

func wantedAt(os string) wantedReport {
	return wantedReport{SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "0123456789abcdef",
		Packages: []string{"pkg:golang/example.com/mod@v1.0.0"}, OS: os}
}

// The platform a miss happened on is the difference between an answer and a
// near-miss, and it is already public in every evidence batch. What must not
// travel is anything finer: a free-text field here would become a
// fingerprinting channel attached to an anonymous id.
func TestWantedReportCarriesTheReportersOS(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct{ sent, want string }{
		{"windows", "windows"},
		{"WINDOWS", "windows"},
		{" linux ", "linux"},
		{"darwin", "darwin"},
		{"", ""},
	} {
		rows, err := rowsForWantedReport(wantedAt(tc.sent), now)
		if err != nil {
			t.Fatalf("os %q: %v", tc.sent, err)
		}
		if rows[0].TargetOS != tc.want {
			t.Errorf("os %q recorded as %q, want %q", tc.sent, rows[0].TargetOS, tc.want)
		}
	}

	for _, bad := range []string{"win32", "ubuntu-22.04-corp-image", "linux; build=nightly", "plan9"} {
		if _, err := rowsForWantedReport(wantedAt(bad), now); err == nil {
			t.Errorf("os %q was accepted; only a fixed public vocabulary may travel", bad)
		}
	}
}
