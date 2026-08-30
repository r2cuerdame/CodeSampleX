package verifier

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// dumpLine builds one line of the production dump format from a raw receipt
// document, so a test can hand the audit a document its schema does not fully
// carry — which is the only way to make the round-trip check say anything.
func dumpLine(t *testing.T, receipt map[string]any) string {
	t.Helper()
	row := map[string]any{
		"receiptId": "receipt:1", "peerId": "peer-1", "sampleId": "sha256:aa",
		"envHash": "env-1", "createdAt": "2026-08-29T00:00:00Z", "receipt": receipt,
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// The round-trip check exists to say the other checks read the WHOLE stored
// document. It could not: it re-encoded the already-decoded struct and
// compared it with itself, so whatever the schema had dropped was gone before
// it ran. Stubbing it to `return true` left every test in this package
// passing.
//
// A document carrying a field this schema does not know must now fail it.
func TestTheRoundTripCheckNoticesAFieldTheSchemaDropped(t *testing.T) {
	base := map[string]any{
		"schemaVersion": 2,
		"stages":        map[string]any{"resolve": "PASS", "contract": "PASS"},
	}
	clean, err := ReadReceiptDump(strings.NewReader(dumpLine(t, base)))
	if err != nil {
		t.Fatal(err)
	}
	if got := AuditReceipts(clean); got.CanonicalRoundTrip != 1 {
		t.Fatalf("a document the schema carries whole failed the round trip: %+v", got)
	}

	extended := map[string]any{}
	for k, v := range base {
		extended[k] = v
	}
	extended["somethingTheSchemaDoesNotCarry"] = "and therefore silently loses"
	lossy, err := ReadReceiptDump(strings.NewReader(dumpLine(t, extended)))
	if err != nil {
		t.Fatal(err)
	}
	if got := AuditReceipts(lossy); got.CanonicalRoundTrip != 0 {
		t.Errorf("a dropped field went unnoticed: canonicalRoundTrip = %d of %d; "+
			"the other checks would have run against a subset of what production stored",
			got.CanonicalRoundTrip, got.Receipts)
	}
}

// The audit re-derives the server's answer, so it has to use the server's
// rule. Its own copy demanded a tag before the digest, which the server does
// not, so a reference production had ACCEPTED was counted here as unpinned.
func TestTheAuditUsesTheServersOwnPinShape(t *testing.T) {
	for _, ref := range []string{
		"ghcr.io/r2cuerdame/csx-verifier@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/r2cuerdame/csx-verifier:v1@sha256:" + strings.Repeat("b", 64),
	} {
		if !domain.PinnedImageReference.MatchString(ref) {
			t.Errorf("the server admits %q and the audit would call it unpinned", ref)
		}
	}
	for _, ref := range []string{
		"ghcr.io/r2cuerdame/csx-verifier:v1",
		"ghcr.io/r2cuerdame/csx-verifier@sha256:short",
		"has a space@sha256:" + strings.Repeat("c", 64),
	} {
		if domain.PinnedImageReference.MatchString(ref) {
			t.Errorf("%q is not digest-pinned but the shape accepted it", ref)
		}
	}
}
