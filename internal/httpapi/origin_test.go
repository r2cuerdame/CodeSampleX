package httpapi

import (
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// CROSS_PASS asserts the one thing a publisher cannot manufacture alone:
// that somebody else reproduced it. The origin was taken as whoever wrote
// the FIRST row, so a stranger could become the origin by filing a FAILING
// receipt — and the author's own passing receipt then counted as the
// independent confirmation.
//
// One real party, one failed run from anybody, and the sample claimed it
// had been cross-verified.
func TestAStrangersFailureCannotMakeTheAuthorTheCrossVerifier(t *testing.T) {
	now := time.Now().UTC()
	rows := []serverstore.ReceiptRow{
		{PeerID: "ed25519:stranger", ContractResult: string(domain.ResultFail), CreatedAt: now.Add(-time.Hour)},
		{PeerID: "ed25519:author", ContractResult: string(domain.ResultPass), CreatedAt: now},
	}
	if got := sampleStatusFromReceipts("PUBLISHED", rows, now); got == "CROSS_PASS" {
		t.Error("one passing peer produced CROSS_PASS: the author cross-verified themselves")
	}
}

// Two peers that actually passed still earn it.
func TestTwoPassingPeersStillEarnCrossPass(t *testing.T) {
	now := time.Now().UTC()
	rows := []serverstore.ReceiptRow{
		{PeerID: "ed25519:author", ContractResult: string(domain.ResultPass), CreatedAt: now.Add(-time.Hour)},
		{PeerID: "ed25519:other", ContractResult: string(domain.ResultPass), CreatedAt: now},
	}
	if got := sampleStatusFromReceipts("PUBLISHED", rows, now); got != "CROSS_PASS" {
		t.Errorf("status = %q, want CROSS_PASS for two independent passes", got)
	}
}
