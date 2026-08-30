package verifier

import (
	"os"
	"sort"
	"testing"
)

// TestProductionReceiptsRanPublishedDigests re-runs the R2C-81 audit over a
// read-only dump of production receipts.
//
// Skipped without a dump, because production data is not in this repo and
// never will be. The point is that the CHECKS are, so the answer is a command
// and not a fresh throwaway script: the 2026-08-23, 08-24 and 08-29 runs each
// rewrote the same six comparisons by hand, and only the numbers survived.
//
// Producing the dump is one read-only query, in docs/operations.md under
// "Re-verifying that production ran published digests".
func TestProductionReceiptsRanPublishedDigests(t *testing.T) {
	path := os.Getenv("CSX_RECEIPT_DUMP")
	if path == "" {
		t.Skip("set CSX_RECEIPT_DUMP to a production receipt dump " +
			"(docs/operations.md, \"Re-verifying that production ran published digests\")")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	rows, err := ReadReceiptDump(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("the dump is empty; an audit of nothing is not evidence")
	}
	got := AuditReceipts(rows)

	first, last := rows[0].CreatedAt, rows[0].CreatedAt
	for _, r := range rows {
		if r.CreatedAt < first {
			first = r.CreatedAt
		}
		if r.CreatedAt > last {
			last = r.CreatedAt
		}
	}
	t.Logf("receipts=%d withImage=%d window=%s..%s", got.Receipts, got.WithImage, first, last)
	t.Logf("signatureValid=%d peerIdBinding=%d receiptIdMatches=%d canonicalRoundTrip=%d rowBinding=%d",
		got.SignatureValid, got.PeerIDBinding, got.ReceiptIDMatches, got.CanonicalRoundTrip, got.RowBinding)
	t.Logf("digestPinned=%d referenceDigestAgree=%d publishedImage=%d",
		got.DigestPinned, got.ReferenceDigestAgree, got.PublishedImage)
	logCounts(t, "os", got.ByOS)
	logCounts(t, "peer", got.ByPeer)
	logCounts(t, "image", got.ByReference)

	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"signatureValid", got.SignatureValid, got.Receipts},
		{"peerIdBinding", got.PeerIDBinding, got.Receipts},
		{"receiptIdMatches", got.ReceiptIDMatches, got.Receipts},
		{"canonicalRoundTrip", got.CanonicalRoundTrip, got.Receipts},
		{"rowBinding", got.RowBinding, got.Receipts},
		{"digestPinned", got.DigestPinned, got.WithImage},
		{"referenceDigestAgree", got.ReferenceDigestAgree, got.WithImage},
		{"publishedImage", got.PublishedImage, got.WithImage},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	// The three image comparisons above are all "== WithImage", and every one
	// of them holds when WithImage is 0. Run against the first 200 rows of a
	// dump whose peers predate v0.1.43 they therefore passed while proving
	// nothing about the property this test is named for. The question the
	// issue asks is whether production RAN published digests, and a dump in
	// which nothing recorded an image cannot answer it either way.
	if got.WithImage == 0 {
		t.Fatalf("no receipt in this dump of %d recorded a verifier image, so it "+
			"says nothing about whether production ran published digests; "+
			"dump a window that includes v0.1.43 or later peers", got.Receipts)
	}
	if len(got.Problems) > 0 {
		shown := got.Problems
		if len(shown) > 20 {
			shown = shown[:20]
		}
		t.Errorf("%d receipt(s) failed a check: %v", len(got.Problems), shown)
	}
}

func logCounts(t *testing.T, label string, counts map[string]int) {
	t.Helper()
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("%s %s = %d", label, k, counts[k])
	}
}
