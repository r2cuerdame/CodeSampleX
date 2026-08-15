package web

import "testing"

// PUBLISHED was mapped straight to L3_CONTRACT_PASS — "the sample's
// intended behaviour was verified" — on the assumption that publication
// implies a local contract pass. Nothing enforces that: `csx sample
// publish` does not require `csx sample verify`, and a POST to /v1/samples
// needs no receipt at all. So a sample the network had never run carried a
// badge saying its contract had passed, on the page a reader opens
// precisely to decide whether to trust it.
func TestAPublishedSampleWithNoReceiptIsNotBadgedContractPass(t *testing.T) {
	if got := levelBadge("PUBLISHED", false); got == "L3_CONTRACT_PASS" {
		t.Errorf("badge = %s with no passing receipt", got)
	}
	if got := levelBadge("PUBLISHED", true); got != "L3_CONTRACT_PASS" {
		t.Errorf("badge = %s with a passing receipt, want L3_CONTRACT_PASS", got)
	}
	// A cross-verified status is earned by receipts already, so it stands.
	if got := levelBadge("CROSS_PASS", false); got != "L4_CROSS_PASS" {
		t.Errorf("CROSS_PASS badge = %s", got)
	}
}

// anyContractPass reads the contract stage, not the rendered display text.
func TestAnyContractPassReadsTheStage(t *testing.T) {
	if anyContractPass([]receiptView{{Contract: "FAIL"}, {Contract: "SKIPPED"}}) {
		t.Error("a failing and a skipped receipt counted as a pass")
	}
	if !anyContractPass([]receiptView{{Contract: "FAIL"}, {Contract: "PASS"}}) {
		t.Error("a passing receipt was not counted")
	}
	if anyContractPass(nil) {
		t.Error("no receipts counted as a pass")
	}
}
