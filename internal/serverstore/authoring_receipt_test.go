package serverstore

import (
	"testing"
	"time"
)

func TestAuthoringReceiptMatchesExistingASCIIJSONFingerprint(t *testing.T) {
	for _, tc := range []struct{ purl, symbol, axis, job, claim string }{
		{"pkg:npm/example@1.0.0", "plain", "SAMPLE", "6f98d732663be8abce7a025cd3163e93ec8acaec681ed18a7b4bfe14bccf4aac", "d0753c2f1b58d135d5ca5276ee27102c9f346284f5a7bed951cd7c0140e83459"},
		{"pkg:npm/example@1.0.0", "<T>&é🙂 ", "SAMPLE", "aab0976819b3b46dbe7ab521f3744fe78d33db43630ec941f568d536919aa8dd", "25f603def79e2095ad4d3da12e04d03227de941814f9ff5355d985c7428c0bed"},
		{"pkg:npm/example@1.0.0", "quote\" slash\\ newline\n", "EVIDENCE", "cb4683fc52079bcf480967e69e6d048d7f8e851460fa11f6f9896d33bfd261b0", "efdd1203dd3beef14875f319a9ac90ed488d20c4743dff8ac56e4dfabf077e20"},
		{"pkg:npm/example@1.0.0", "DEL and C1", "SAMPLE", "cb20bf75614c7d09a34a46dce134b24910b75d2a78531d60920cfd960af292b2", "a58740008a10d78ac0b3a09ee9e45fd49c821aadf6d3f9fdbfd0c3e07c5accd7"},
	} {
		expires, _ := time.Parse(time.RFC3339, "2026-10-15T05:16:35Z")
		job, claim := AuthoringJobReceiptRefs(tc.purl, tc.symbol, tc.axis, expires)
		if job != tc.job || claim != tc.claim {
			t.Fatalf("canonical fingerprint mismatch: job=%s claim=%s", job, claim)
		}
		offset := expires.In(time.FixedZone("fixture", 9*3600))
		j2, c2 := AuthoringJobReceiptRefs(tc.purl, tc.symbol, tc.axis, offset)
		if j2 != job || c2 != claim {
			t.Fatal("timezone changed stable claim fingerprint")
		}
	}
}
