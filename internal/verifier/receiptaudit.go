package verifier

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// Re-verifying that production really ran pinned images used to be a
// throwaway program.
//
// R2C-81 asks one question — did the workers execute immutable digest-pinned
// images, and do the receipts truthfully record which — and it has now been
// answered three times (2026-08-23: 0 receipts carried an image; 08-24: 431;
// 08-29: 2,279). Each answer was produced by an ad hoc script that decoded a
// dump, re-derived the peer id, re-canonicalised the document and compared
// digests, and each script was thrown away, so the next re-verification
// started from nothing and the numbers could not be reproduced by anyone
// else. The checks below are exactly those, kept.
//
// This is deliberately an AUDIT and not an admission rule. The server accepts
// any well-formed pin on purpose (a worker may run a newer registry than the
// server), so "the executed image is one this build publishes" is a property
// to be measured over stored receipts, not enforced at the door.

// AuditedReceipt is one stored receipt as the store holds it: the signed
// document plus the columns it was indexed by. The columns are carried
// separately on purpose — an audit that reads the id out of the document it
// is checking cannot detect a document that was replaced after storage.
type AuditedReceipt struct {
	ReceiptID string
	PeerID    string
	SampleID  string
	EnvHash   string
	CreatedAt string
	Receipt   domain.VerificationReceipt
	// RawReceipt is the receipt exactly as stored, before this package's
	// schema saw it. Without it the round-trip check below has nothing to
	// compare against and cannot fail.
	RawReceipt []byte
}

// ReceiptAudit counts, per check, how many receipts passed. Counts rather
// than a bool: "all 2,279 verified" and "the one receipt we looked at
// verified" are different evidence, and a total that does not match Receipts
// is itself the finding.
type ReceiptAudit struct {
	Receipts  int
	WithImage int

	// SignatureValid: the stored bytes are the ones the peer's key signed.
	SignatureValid int
	// PeerIDBinding: peerId == "ed25519:" + hex(sha256(peerPubkey))[:16], so
	// the key that signed and the identity claimed are the same peer.
	PeerIDBinding int
	// ReceiptIDMatches: the stored id is the content hash of the stored
	// document. This is what makes the image identity and the verdict parts
	// of ONE hashed document rather than two tables someone joined.
	ReceiptIDMatches int
	// CanonicalRoundTrip: re-canonicalising the document reproduces it, so
	// nothing in the stored JSON is outside the schema these checks read.
	CanonicalRoundTrip int
	// RowBinding: the store's own columns agree with the signed document.
	RowBinding int

	// DigestPinned: reference is <alias>@sha256:<64 hex> — not a mutable tag.
	DigestPinned int
	// ReferenceDigestAgree: the standalone digest field is the reference's.
	ReferenceDigestAgree int
	// PublishedImage: the executed reference is an entry this build's
	// registry publishes, digest included.
	PublishedImage int

	ByOS        map[string]int
	ByPeer      map[string]int
	ByReference map[string]int

	// Problems names every receipt that failed a check, so a non-zero
	// shortfall can be chased instead of merely noticed.
	Problems []string
}

// pinnedImageReference is the server's own admission shape, and now literally
// so rather than by description. The copy that used to be here demanded a tag
// before the digest, which the server does not, so a reference the server had
// admitted could be counted here as unpinned — an audit disagreeing with the
// thing it audits.

// AuditReceipts re-verifies stored receipts offline, using the same code the
// verifier signs with, and reports how many passed each check.
func AuditReceipts(rows []AuditedReceipt) ReceiptAudit {
	a := ReceiptAudit{
		ByOS:        map[string]int{},
		ByPeer:      map[string]int{},
		ByReference: map[string]int{},
	}
	for _, row := range rows {
		r := row.Receipt
		a.Receipts++
		a.ByOS[r.Environment.OS]++
		a.ByPeer[r.PeerID]++
		fail := func(check string) {
			a.Problems = append(a.Problems, check+" "+row.ReceiptID)
		}

		if identity.Verify(r.PeerPubkey, r.PeerSignature, r.SigningBytes()) {
			a.SignatureValid++
		} else {
			fail("signature")
		}
		if peerIDOf(r.PeerPubkey) == r.PeerID {
			a.PeerIDBinding++
		} else {
			fail("peerIdBinding")
		}
		if r.ReceiptID() == row.ReceiptID {
			a.ReceiptIDMatches++
		} else {
			fail("receiptId")
		}
		if canonicalRoundTrips(row.RawReceipt, r) {
			a.CanonicalRoundTrip++
		} else {
			fail("canonicalRoundTrip")
		}
		if row.PeerID == r.PeerID && row.SampleID == r.SampleID && row.EnvHash == r.EnvironmentHash {
			a.RowBinding++
		} else {
			fail("rowBinding")
		}

		img := r.VerifierImage
		if img == nil {
			// Absent is NOT ESTABLISHED, never "the default image": a native
			// run entered no container and a pre-v0.1.43 peer could not
			// record one. Counting it as a pinning failure would invent one.
			continue
		}
		a.WithImage++
		a.ByReference[img.Reference]++
		if domain.PinnedImageReference.MatchString(img.Reference) {
			a.DigestPinned++
		} else {
			fail("notDigestPinned")
		}
		if img.Digest != "" && strings.HasSuffix(img.Reference, "@"+img.Digest) {
			a.ReferenceDigestAgree++
		} else {
			fail("referenceDigestDisagree")
		}
		if published, ok := sandbox.PublishedImage(img.Reference); ok && published.Digest == img.Digest {
			a.PublishedImage++
		} else {
			fail("unpublishedImage")
		}
	}
	return a
}

func peerIDOf(pubkeyB64 string) string {
	pub, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:])[:16]
}

// canonicalRoundTrips reports whether this package's schema read the WHOLE
// stored document. A field the schema does not carry is dropped on decode,
// and dropping it moves the content hash — so a receipt that fails here is
// one whose other checks were run against a subset of what production stored.
//
// It compares the canonical form of the decoded receipt against the canonical
// form of the raw bytes. The version before it compared the decoded struct
// against ITSELF re-encoded and re-decoded, which is a tautology: whatever the
// schema had already dropped was gone before the function was called, so it
// could not fail. Stubbing it to `return true` left every test in this
// package passing, which is how that was found.
func canonicalRoundTrips(raw []byte, r domain.VerificationReceipt) bool {
	if len(raw) == 0 {
		// Nothing to compare against. A check with no evidence must not
		// report a pass.
		return false
	}
	var stored any
	if err := json.Unmarshal(raw, &stored); err != nil {
		return false
	}
	var kept any
	if err := json.Unmarshal(domain.MustCanonicalJSON(r), &kept); err != nil {
		return false
	}
	// Subset, not equality. The struct emits every field the schema declares,
	// including the ones this document left out, so equality would fail every
	// receipt that omits an optional field. What matters is the other
	// direction: nothing the document CARRIED may be missing afterwards.
	return survivesDecoding(stored, kept)
}

// survivesDecoding reports whether every value present in the stored document
// is present and identical in what the schema kept.
func survivesDecoding(stored, kept any) bool {
	switch want := stored.(type) {
	case map[string]any:
		got, ok := kept.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range want {
			other, present := got[k]
			if !present || !survivesDecoding(v, other) {
				return false
			}
		}
		return true
	case []any:
		got, ok := kept.([]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for i := range want {
			if !survivesDecoding(want[i], got[i]) {
				return false
			}
		}
		return true
	default:
		return string(domain.MustCanonicalJSON(stored)) == string(domain.MustCanonicalJSON(kept))
	}
}

// ReadReceiptDump decodes a production receipt dump: one base64-encoded JSON
// row per line, blank lines ignored.
//
// Base64 because `COPY (...) TO STDOUT` uses Postgres' text format, which
// escapes backslashes — every \" inside a stored manifest comes back broken,
// and the corruption looks like a malformed receipt rather than a bad export.
// The producing query lives in docs/operations.md.
func ReadReceiptDump(r io.Reader) ([]AuditedReceipt, error) {
	var rows []AuditedReceipt
	sc := bufio.NewScanner(r)
	// A receipt with resolvedPackages runs to tens of KB; the default 64 KB
	// token limit would stop the scan mid-dump and return a short, clean-
	// looking answer.
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			return nil, fmt.Errorf("receipt dump line %d: base64: %w", line, err)
		}
		// The receipt is taken as written first and decoded from those
		// bytes second. Two fields cannot share one json tag — Go drops
		// both — so the raw document and the typed one come from one read
		// of the row and one of the receipt, not from two tags.
		var row struct {
			ReceiptID string          `json:"receiptId"`
			PeerID    string          `json:"peerId"`
			SampleID  string          `json:"sampleId"`
			EnvHash   string          `json:"envHash"`
			CreatedAt string          `json:"createdAt"`
			Receipt   json.RawMessage `json:"receipt"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, fmt.Errorf("receipt dump line %d: json: %w", line, err)
		}
		var receipt domain.VerificationReceipt
		if err := json.Unmarshal(row.Receipt, &receipt); err != nil {
			return nil, fmt.Errorf("receipt dump line %d: receipt: %w", line, err)
		}
		rows = append(rows, AuditedReceipt{
			ReceiptID: row.ReceiptID, PeerID: row.PeerID, SampleID: row.SampleID,
			EnvHash: row.EnvHash, CreatedAt: row.CreatedAt, Receipt: receipt,
			RawReceipt: append([]byte(nil), row.Receipt...),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("receipt dump: %w", err)
	}
	return rows, nil
}
