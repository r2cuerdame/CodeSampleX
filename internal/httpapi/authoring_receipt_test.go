package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type receiptFaultStore struct {
	*serverstore.Fake
	fail string
}

func (s *receiptFaultStore) SaveAuthoringDraft(ctx context.Context, r serverstore.AuthoringDraftRow) error {
	if s.fail == "draft" {
		return errors.New("synthetic private failure")
	}
	return s.Fake.SaveAuthoringDraft(ctx, r)
}
func (s *receiptFaultStore) SaveCase(ctx context.Context, c domain.Case) error {
	if s.fail == "case" {
		return errors.New("synthetic private failure")
	}
	return s.Fake.SaveCase(ctx, c)
}

type receiptLogBuffer struct {
	sync.Mutex
	b bytes.Buffer
}

func (b *receiptLogBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.b.Write(p)
}
func (b *receiptLogBuffer) String() string { b.Lock(); defer b.Unlock(); return b.b.String() }

func TestAuthoringSubmissionReceiptSeparatesAcceptanceFromPersistence(t *testing.T) {
	for _, tc := range []struct {
		name, status, fail string
		http               int
		accepted, saved    bool
	}{
		{"accepted", "LOCAL", "", 201, true, true},
		{"save_failed", "LOCAL", "draft", 500, false, false},
		{"saved_but_not_accepted", "LOCAL_PASS", "case", 500, false, true},
		{"wrong_assignment", "LOCAL", "mismatch", 409, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, _ := newTestServer(t, func(d *Deps) { d.Store = &receiptFaultStore{Fake: d.Store.(*serverstore.Fake), fail: tc.fail} })
			const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
			sum := sha256.Sum256([]byte(token))
			const sessionID = "synthetic-private-session"
			const symbol = "synthetic.private.receipt"
			if err := store.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{TokenHash: hex.EncodeToString(sum[:]), SessionID: sessionID, Label: "synthetic-private-label", Model: "agy", IssuedAt: testNow, IdleExpiresAt: testNow.Add(time.Hour)}}, testNow); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordWanted(t.Context(), testNow.Format("2006-01-02"), "0123456789abcdef", []serverstore.WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: symbol}}); err != nil {
				t.Fatal(err)
			}
			next, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", strings.NewReader(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
			next.Header.Set("Authorization", "Bearer "+token)
			assigned, err := http.DefaultClient.Do(next)
			if err != nil {
				t.Fatal(err)
			}
			var work struct {
				Status string
				Work   struct {
					Package, Symbol, Axis string
					LeaseExpiresAt        time.Time
				}
			}
			if err := json.NewDecoder(assigned.Body).Decode(&work); err != nil {
				t.Fatal(err)
			}
			assigned.Body.Close()
			if work.Status != "ASSIGNED" {
				t.Fatal("fixture was not assigned")
			}
			job, claim := serverstore.AuthoringJobReceiptRefs(work.Work.Package, work.Work.Symbol, work.Work.Axis, work.Work.LeaseExpiresAt)
			manifest := testManifest()
			manifest.Symbols = []string{symbol}
			if tc.fail == "mismatch" {
				manifest.Symbols = []string{"other.synthetic.symbol"}
			}
			artifact := buildArtifact(t, manifest, map[string]string{"test/contract.mjs": "process.exit(0)\n"})
			sampleID := domain.SHA256Hex(artifact)
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			raw, _ := json.Marshal(manifest)
			_ = mw.WriteField("manifest", string(raw))
			_ = mw.WriteField("sampleId", sampleID)
			_ = mw.WriteField("localStatus", tc.status)
			f, _ := mw.CreateFormFile("artifact", "sample.tar.gz")
			_, _ = f.Write(artifact)
			_ = mw.Close()
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/drafts", &body)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			req.Header.Set("Authorization", "Bearer "+token)
			captured := &receiptLogBuffer{}
			previous := log.Writer()
			log.SetOutput(captured)
			defer log.SetOutput(previous)
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != tc.http {
				t.Fatalf("HTTP=%d", response.StatusCode)
			}
			var receipt string
			for _, line := range strings.Split(captured.String(), "\n") {
				if strings.Contains(line, "authoring receipt event=submission job_ref="+job+" claim_ref="+claim) {
					if receipt != "" {
						t.Fatal("duplicate receipt")
					}
					receipt = line
				}
			}
			if receipt == "" {
				t.Fatal("missing server receipt")
			}
			want := "accepted=" + map[bool]string{true: "true", false: "false"}[tc.accepted] + " draft_saved=" + map[bool]string{true: "true", false: "false"}[tc.saved]
			if !strings.HasSuffix(receipt, want) {
				t.Fatalf("receipt=%q want=%s", receipt, want)
			}
			for _, private := range []string{token, sessionID, "synthetic-private-label", symbol, sampleID, "synthetic private failure"} {
				if strings.Contains(receipt, private) {
					t.Fatal("private value in receipt")
				}
			}
		})
	}
}
