package web

import (
	"net/http"
	"strings"
	"testing"
)

const sampleImageDigest = "sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32"

// The page tells a reader the contract ran in a pinned container. Until the
// receipt named the image, that was a claim the page could not show the
// evidence for — the environment line says musl and node 22, and any number
// of different images produce exactly that.
func TestSamplePageShowsWhichImageRanTheContract(t *testing.T) {
	mux, st := newTestMux(t, nil)
	st.receipts["sha256:d1e2f3"] = []string{`{
	  "schemaVersion": 2,
	  "sampleId": "sha256:d1e2f3",
	  "caseId": "case:sha256:9999",
	  "environmentHash": "sha256:eeee",
	  "environment": {"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"amd64",
	    "runtime":"node","runtimeVersion":"22.18","executionContext":"node"},
	  "stages": {"resolve":"PASS","compile":"PASS","contract":"PASS"},
	  "verifierImage": {"reference":"node:22-alpine@` + sampleImageDigest + `","digest":"` + sampleImageDigest + `"},
	  "verifierAdapter": "node-typescript@1",
	  "sandboxCapability": "CONTAINER_RUN",
	  "logsDigest": "sha256:ffff",
	  "createdAt": "2026-08-02T00:00:00Z",
	  "peerId": "ed25519:0011223344556677",
	  "peerPubkey": "cHVi",
	  "peerSignature": "c2ln"
	}`}

	rec := get(t, mux, "/samples/sha256:d1e2f3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	// The whole reference is what a reader re-runs, so it has to be on the
	// page in full; the cell shows a readable short form of it.
	mustContain(t, body, `title="node:22-alpine@`+sampleImageDigest+`"`)
	mustContain(t, body, `node:22-alpine@sha256:c610fcdfb1d5`)
}

// A receipt that establishes no image — every receipt signed before the
// field existed — must show nothing rather than a default. Naming an image
// the run may never have used is the error this whole change removes.
func TestSamplePageInventsNoImageForAReceiptWithoutOne(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/samples/sha256:d1e2f3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "runimage") {
		t.Error("a receipt establishing no image was rendered with one")
	}
}

func TestShortImageRefKeepsTheAliasAndTrimsOnlyTheDigest(t *testing.T) {
	got := shortImageRef("node:22-alpine@" + sampleImageDigest)
	if !strings.HasPrefix(got, "node:22-alpine@sha256:c610fcdfb1d5") {
		t.Errorf("shortImageRef = %q", got)
	}
	if got == "node:22-alpine@"+sampleImageDigest {
		t.Error("the digest was not shortened at all")
	}
	// Anything that is not a digest reference is left exactly as it is
	// rather than being cut somewhere arbitrary.
	if got := shortImageRef("node:22-alpine"); got != "node:22-alpine" {
		t.Errorf("shortImageRef(tag) = %q, want it untouched", got)
	}
}
