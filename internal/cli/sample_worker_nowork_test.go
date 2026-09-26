package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// These fixtures exercise the wire response through the public next command.
func TestSampleWorkerNextNoWorkDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, response, want string
	}{
		{
			name:     "server diagnostics",
			response: `{"status":"NO_WORK","reason":"no candidate is eligible for this request","funnel":{"wanted":12,"wantedEligible":0,"expansion":200,"expansionEligible":117,"offered":0}}`,
			want:     "NO_WORK: no eligible SAMPLE new claim is available for this worker.\nNO_WORK: reason=\"no candidate is eligible for this request\" wanted=12/0 expansion=200/117 offered=0\n",
		},
		{
			name:     "old server omits diagnostics",
			response: `{"status":"NO_WORK"}`,
			want:     "NO_WORK: no eligible SAMPLE new claim is available for this worker.\nNO_WORK: reason=\"unknown\" wanted=unknown/unknown expansion=unknown/unknown offered=unknown\n",
		},
		{
			name:     "partially populated funnel",
			response: `{"status":"NO_WORK","reason":"no candidates","funnel":{"wanted":0,"expansionEligible":3}}`,
			want:     "NO_WORK: no eligible SAMPLE new claim is available for this worker.\nNO_WORK: reason=\"no candidates\" wanted=0/unknown expansion=unknown/3 offered=unknown\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, out, stderr := reservationNextHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tc.response)
			})
			if code := sampleWorkerNext(context.Background(), []string{"--server", url, "--token", "csx_author_v1_nowork", "--reservation", "SAMPLE"}); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("stdout = %q, want %q", got, tc.want)
			}
		})
	}
}

// Farm health's author_result_kind matches complete journal messages by equality.
// Keep its current SAMPLE marker byte-for-byte while emitting diagnostics separately.
func TestSampleWorkerNextNoWorkFarmCompatibility(t *testing.T) {
	const farmHealthSampleMarker = "NO_WORK: no eligible SAMPLE new claim is available for this worker."
	for _, tc := range []struct {
		name, response, diagnostic string
	}{
		{
			name: "populated",
			response: `{"status":"NO_WORK","reason":"no candidate is eligible for this request","funnel":{"wanted":12,"wantedEligible":0,"expansion":200,"expansionEligible":117,"offered":0}}`,
			diagnostic: `NO_WORK: reason="no candidate is eligible for this request" wanted=12/0 expansion=200/117 offered=0`,
		},
		{
			name: "missing",
			response: `{"status":"NO_WORK"}`,
			diagnostic: `NO_WORK: reason="unknown" wanted=unknown/unknown expansion=unknown/unknown offered=unknown`,
		},
		{
			name: "partial",
			response: `{"status":"NO_WORK","reason":"no candidates","funnel":{"wanted":0,"expansionEligible":3}}`,
			diagnostic: `NO_WORK: reason="no candidates" wanted=0/unknown expansion=unknown/3 offered=unknown`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, out, stderr := reservationNextHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tc.response)
			})
			if code := sampleWorkerNext(context.Background(), []string{"--server", url, "--token", "csx_author_v1_nowork", "--reservation", "SAMPLE"}); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
			lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			foundMarker, foundDiagnostic := false, false
			for _, line := range lines {
				foundMarker = foundMarker || line == farmHealthSampleMarker
				foundDiagnostic = foundDiagnostic || line == tc.diagnostic
			}
			if !foundMarker {
				t.Errorf("Farm health marker absent from stdout lines %q", lines)
			}
			if !foundDiagnostic {
				t.Errorf("server diagnostic %q absent from stdout lines %q", tc.diagnostic, lines)
			}
			if len(lines) != 2 || lines[0] != farmHealthSampleMarker || lines[1] != tc.diagnostic {
				t.Errorf("stdout lines = %q, want legacy Farm marker then server diagnostic", lines)
			}
		})
	}
}
