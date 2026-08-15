package web

import (
	"encoding/json"
	"testing"
)

// The "Peers" column read uniquePeerBuckets, which the producer documents
// as an OBSERVATION-side count. A symbol proved by five independent peers
// and used by nobody yet — the normal shape for a freshly seeded package —
// therefore printed 0 under a column a reader uses to judge exactly that
// independence, while the Verified column beside it said 5.
//
// The two counts are different classes of evidence and are never summed;
// they are shown side by side.
func TestVerifyingPeersAreDecodedFromTheSnapshot(t *testing.T) {
	raw := `{
	  "contextLabel": "node",
	  "confidence": "HIGH",
	  "uniquePeerBuckets": 0,
	  "byStage": {"CONTRACT": {"pass": 5}},
	  "verificationCounts": {"SAMPLE_VERIFICATION": 5, "distinctVerifyingPeers": 5}
	}`
	var row snapshotRow
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		t.Fatal(err)
	}
	if got := row.VerificationCounts["distinctVerifyingPeers"]; got != 5 {
		t.Errorf("distinctVerifyingPeers = %d, want 5 — the store's own count was dropped", got)
	}
	if row.UniquePeerBuckets != 0 {
		t.Errorf("uniquePeerBuckets = %d, want 0 — it is an observation count and stays one", row.UniquePeerBuckets)
	}
}
