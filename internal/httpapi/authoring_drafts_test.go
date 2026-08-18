package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestAuthoringDraftUploadStaysPrivate(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	sum := sha256.Sum256([]byte(token))
	now := testNow
	if err := store.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: hex.EncodeToString(sum[:]), SessionID: "draft-worker-1", Label: "worker-01",
		Model: "claude-haiku", Reasoning: "low", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWanted(t.Context(), now.Format("2006-01-02"), "0123456789abcdef", []serverstore.WantedRow{{
		Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post",
	}}); err != nil {
		t.Fatal(err)
	}
	next, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", nil)
	next.Header.Set("Authorization", "Bearer "+token)
	nextResp, err := http.DefaultClient.Do(next)
	if err != nil {
		t.Fatal(err)
	}
	defer nextResp.Body.Close()
	var assigned struct {
		Status string                           `json:"status"`
		Work   struct{ Package, Symbol string } `json:"work"`
	}
	if err := json.NewDecoder(nextResp.Body).Decode(&assigned); err != nil || assigned.Status != "ASSIGNED" || assigned.Work.Package != "pkg:npm/axios@1.12.0" || assigned.Work.Symbol != "axios.post" {
		t.Fatalf("assigned work = %+v, err=%v", assigned, err)
	}
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, map[string]string{"test/contract.mjs": "process.exit(0)\n"})
	sampleID := domain.SHA256Hex(artifact)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	manifestJSON, _ := json.Marshal(manifest)
	_ = mw.WriteField("manifest", string(manifestJSON))
	_ = mw.WriteField("sampleId", sampleID)
	_ = mw.WriteField("localStatus", "LOCAL_PASS")
	_ = mw.WriteField("computerName", "worker-host")
	fw, _ := mw.CreateFormFile("artifact", "sample.tar.gz")
	_, _ = fw.Write(artifact)
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/drafts", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("draft status = %d", resp.StatusCode)
	}
	var submitted map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil || submitted["status"] != "CROSS_PENDING" {
		t.Fatalf("draft response = %+v, err=%v", submitted, err)
	}
	drafts, err := store.ListAuthoringDrafts(t.Context(), 10)
	if err != nil || len(drafts) != 1 || drafts[0].SampleID != sampleID || drafts[0].LocalStatus != "LOCAL_PASS" {
		t.Fatalf("drafts = %+v, err=%v", drafts, err)
	}
	row, ok, err := store.GetSample(t.Context(), sampleID)
	if err != nil || !ok || row.Status != "DRAFT" || !row.Quarantined {
		t.Fatalf("private verification row = %+v ok=%v err=%v", row, ok, err)
	}
	jobs, err := store.JobsForSample(t.Context(), sampleID)
	if err != nil || len(jobs) != 1 || jobs[0].Reason != "cross" || jobs[0].Status != "open" {
		t.Fatalf("draft jobs = %+v, err=%v", jobs, err)
	}
	public, err := http.Get(srv.URL + "/v1/samples/" + sampleID)
	if err != nil {
		t.Fatal(err)
	}
	defer public.Body.Close()
	if public.StatusCode != http.StatusNotFound {
		t.Fatalf("private draft public status = %d", public.StatusCode)
	}

	priv, peerID := newPeer(t)
	if claimed, err := store.ClaimJob(t.Context(), jobs[0].ID, peerID); err != nil || !claimed {
		t.Fatalf("claim draft job = %v, %v", claimed, err)
	}
	download, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/samples/"+sampleID+"/artifact", nil)
	download.Header.Set(domain.VerificationJobIDHeader, fmt.Sprintf("%d", jobs[0].ID))
	download.Header.Set(domain.VerificationPeerIDHeader, peerID)
	downloaded, err := http.DefaultClient.Do(download)
	if err != nil {
		t.Fatal(err)
	}
	defer downloaded.Body.Close()
	gotArtifact, _ := io.ReadAll(downloaded.Body)
	if downloaded.StatusCode != http.StatusOK || !bytes.Equal(gotArtifact, artifact) {
		t.Fatalf("claimed draft download status=%d bytes=%d", downloaded.StatusCode, len(gotArtifact))
	}

	verifiedEnv := nodeEnv("esm")
	verifiedEnv.OS = "linux"
	verifiedEnv.Virtualization = "container"
	receipt := signedReceipt(t, priv, sampleID, verifiedEnv, "PASS")
	receiptBody, _ := json.Marshal(receipt)
	verification, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/verifications", bytes.NewReader(receiptBody))
	verification.Header.Set("Content-Type", "application/json")
	verification.Header.Set(domain.VerificationJobIDHeader, fmt.Sprintf("%d", jobs[0].ID))
	verified, err := http.DefaultClient.Do(verification)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Body.Close()
	if verified.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(verified.Body)
		t.Fatalf("cross verification status=%d body=%s", verified.StatusCode, body)
	}
	published, err := http.Get(srv.URL + "/v1/samples/" + sampleID)
	if err != nil {
		t.Fatal(err)
	}
	defer published.Body.Close()
	if published.StatusCode != http.StatusOK {
		t.Fatalf("cross-passed draft public status=%d", published.StatusCode)
	}
	var publicMeta struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(published.Body).Decode(&publicMeta); err != nil || publicMeta.Status != "CROSS_PASS" {
		t.Fatalf("public draft metadata=%+v err=%v", publicMeta, err)
	}
}

func TestAuthoringDraftRejectsSampleWithoutAssignedWantedWork(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	sum := sha256.Sum256([]byte(token))
	if err := store.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: hex.EncodeToString(sum[:]), SessionID: "unassigned-writer", Label: "worker-02",
		Model: "agy", Reasoning: "auto", IssuedAt: testNow, IdleExpiresAt: testNow.Add(time.Hour),
	}}, testNow); err != nil {
		t.Fatal(err)
	}
	manifest := testManifest()
	artifact := buildArtifact(t, manifest, map[string]string{"test/contract.mjs": "process.exit(0)\n"})
	sampleID := domain.SHA256Hex(artifact)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	manifestJSON, _ := json.Marshal(manifest)
	_ = mw.WriteField("manifest", string(manifestJSON))
	_ = mw.WriteField("sampleId", sampleID)
	_ = mw.WriteField("localStatus", "LOCAL_PASS")
	fw, _ := mw.CreateFormFile("artifact", "sample.tar.gz")
	_, _ = fw.Write(artifact)
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/drafts", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("unassigned draft status = %d, want 409", resp.StatusCode)
	}
	if _, ok, err := store.GetSample(t.Context(), sampleID); err != nil || ok {
		t.Fatalf("unassigned sample entered verification store: ok=%v err=%v", ok, err)
	}
	if drafts, err := store.ListAuthoringDrafts(t.Context(), 10); err != nil || len(drafts) != 0 {
		t.Fatalf("unassigned draft entered inbox: drafts=%+v err=%v", drafts, err)
	}
}
