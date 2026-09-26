package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Regression for #516: a NO_WORK poll must carry the server's reason and funnel
// counts instead of only the fixed legacy sentence, with unknown for absent values.
func TestSampleWorkerNextNoWorkReportsServerReason(t *testing.T) {
	for _, tc := range []struct {
		name, response, diagnostic string
	}{
		{
			name:       "server diagnostics",
			response:   `{"status":"NO_WORK","reason":"no candidate is eligible for this request","funnel":{"wanted":12,"wantedEligible":0,"expansion":200,"expansionEligible":117,"offered":0}}`,
			diagnostic: `NO_WORK: reason="no candidate is eligible for this request" wanted=12/0 expansion=200/117 offered=0`,
		},
		{
			name:       "old server omits diagnostics",
			response:   `{"status":"NO_WORK"}`,
			diagnostic: `NO_WORK: reason="unknown" wanted=unknown/unknown expansion=unknown/unknown offered=unknown`,
		},
		{
			name:       "partially populated funnel",
			response:   `{"status":"NO_WORK","reason":"no candidates","funnel":{"wanted":0,"expansionEligible":3}}`,
			diagnostic: `NO_WORK: reason="no candidates" wanted=0/unknown expansion=unknown/3 offered=unknown`,
		},
	} {
		for _, reserved := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reserved=%v", tc.name, reserved), func(t *testing.T) {
				url, out, stderr := reservationNextHarness(t, func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprint(w, tc.response)
				})
				args := []string{"--server", url, "--token", "csx_author_v1_nowork"}
				if reserved {
					args = append(args, "--reservation", "SAMPLE")
				}
				if code := sampleWorkerNext(context.Background(), args); code != 0 {
					t.Fatalf("exit=%d stderr=%s", code, stderr)
				}
				got := out.String()
				wantSuffix := tc.diagnostic + "\n"
				if !strings.HasSuffix(got, wantSuffix) {
					t.Errorf("sampleWorkerNext stdout = %q; want server reason/funnel suffix %q", got, wantSuffix)
				}
				if count := strings.Count(got, "NO_WORK: reason="); count != 1 {
					t.Errorf("sampleWorkerNext stdout = %q; want exactly one server reason/funnel line, got %d", got, count)
				}
			})
		}
	}
}
