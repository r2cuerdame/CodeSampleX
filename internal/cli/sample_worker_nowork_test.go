package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Non-regression guard: Farm health's author_result_kind matches complete journal
// messages by equality, so the legacy NO_WORK sentence must stay the first line
// byte-for-byte. This holds on the pre-#516 baseline too; the reason/funnel
// regression lives in sample_worker_nowork_reason_test.go.
func TestSampleWorkerNextNoWorkFarmCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, response string
	}{
		{name: "populated", response: `{"status":"NO_WORK","reason":"no candidate is eligible for this request","funnel":{"wanted":12,"wantedEligible":0,"expansion":200,"expansionEligible":117,"offered":0}}`},
		{name: "missing", response: `{"status":"NO_WORK"}`},
		{name: "partial", response: `{"status":"NO_WORK","reason":"no candidates","funnel":{"wanted":0,"expansionEligible":3}}`},
	} {
		for _, reserved := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reserved=%v", tc.name, reserved), func(t *testing.T) {
				url, out, stderr := reservationNextHarness(t, func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprint(w, tc.response)
				})
				args := []string{"--server", url, "--token", "csx_author_v1_nowork"}
				farmHealthMarker := "NO_WORK: no runnable Sample, Evidence, Dependency, or CLI gap is available for this worker."
				if reserved {
					args = append(args, "--reservation", "SAMPLE")
					farmHealthMarker = "NO_WORK: no eligible SAMPLE new claim is available for this worker."
				}
				if code := sampleWorkerNext(context.Background(), args); code != 0 {
					t.Fatalf("exit=%d stderr=%s", code, stderr)
				}
				lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
				if lines[0] != farmHealthMarker {
					t.Errorf("stdout lines = %q, want Farm health marker %q first", lines, farmHealthMarker)
				}
			})
		}
	}
}
