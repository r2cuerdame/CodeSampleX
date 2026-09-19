package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
	"github.com/r2cuerdame/codesamplex/internal/httpapi"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/storage/blob"
)

// The Phase 0 execution (#444). Everything above this test proves the
// pipeline's pieces with a stubbed executor; this one runs the checked-in
// reproducers for real: the server mux with its lease, sample, receipt and
// run checks, the local Docker sandbox for every probe, and the evaluator
// deciding each record from what actually ran. It is the measurement the
// issue asks for before anything scales, so it is gated the way the other
// real-Docker tests are (CSX_TEST_DOCKER=1) and it writes what it measured
// to CSX_FIX_PHASE0_EVIDENCE when that names a file.
//
// The assertions are deliberately about integrity, not about how many
// claims turned out true: every candidate must leave CLAIMED_FIX through a
// run this server checked against a receipt, no record may be verified
// without a FAIL receipt and a PASS receipt, and a NOT_REPRODUCED must stay
// exactly that.

const fixPhase0Root = "../../seeds/fix-claims/reproducers"

func TestPhase0ReproducersRunEndToEnd(t *testing.T) {
	if os.Getenv("CSX_TEST_DOCKER") != "1" {
		t.Skip("set CSX_TEST_DOCKER=1 to run the Phase 0 reproducers in the real docker sandbox")
	}
	ctx := context.Background()
	if sandbox.Detect(ctx) != domain.CapContainerRun {
		t.Skip("docker daemon not available")
	}
	repros, err := loadFixReproducers(fixPhase0Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repros) < 10 {
		t.Fatalf("%d reproducers checked in, want at least 10", len(repros))
	}
	// A credential-free home of its own: the probe writes samples, receipts
	// and an identity there, none of which belong in the developer's.
	t.Setenv("CSX_HOME", t.TempDir())
	verifierRunner, verifierCapability = nil, ""

	store := serverstore.NewFake()
	blobs, err := blob.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.NewMux(httpapi.Deps{
		Store: store, Blobs: blobs,
		Cfg:       serverstore.ServerConfig{PublicCheck: "trust", Publishing: "open"},
		Now:       time.Now,
		PeerProbe: func(context.Context, string, int) bool { return true },
	}))
	defer srv.Close()
	token := "csx_author_v1_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("p", 32)))
	sum := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	if err := store.IssueAuthoringSessions(ctx, []serverstore.AuthoringSessionRow{{
		TokenHash: hex.EncodeToString(sum[:]), SessionID: "phase0-worker", Label: "phase0-worker",
		Model: "none", Reasoning: "none", IssuedAt: now, IdleExpiresAt: now.Add(6 * time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	oldClient := fixClaimsClient
	fixClaimsClient = srv.Client()
	t.Cleanup(func() { fixClaimsClient = oldClient })

	var candidates []fixclaims.Candidate
	for _, r := range repros {
		candidates = append(candidates, r.candidates()...)
	}
	payload, _ := json.Marshal(map[string]any{"schemaVersion": 1, "candidates": candidates})
	code, body, ok := fixClaimsCall(ctx, http.MethodPost, srv.URL, token, "/v1/fix-claims/candidates", payload)
	if !ok || code != http.StatusOK {
		t.Fatalf("ingest: %d %v", code, body)
	}
	accepted, _ := body["accepted"].([]any)
	if rejected, _ := body["rejected"].([]any); len(rejected) != 0 || len(accepted) != len(candidates) {
		t.Fatalf("ingest accepted %d of %d; rejected %v", len(accepted), len(candidates), rejected)
	}
	var ids []int64
	for _, a := range accepted {
		ids = append(ids, int64(a.(map[string]any)["id"].(float64)))
	}

	stdout, stderr := captureFixClaims(t)
	started := time.Now()
	if code := fixClaimsWork(ctx, []string{"--server", srv.URL, "--token", token, "--repro-root", fixPhase0Root, "--os", "linux"}); code != 0 {
		t.Fatalf("work loop exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	wall := time.Since(started)
	t.Logf("work loop:\n%s", stdout.String())
	if !strings.Contains(stdout.String(), "NO_WORK after") {
		t.Fatalf("the loop stopped before the queue was empty:\n%s\n%s", stdout.String(), stderr.String())
	}

	type record struct {
		ID                  int64                         `json:"id"`
		Status              fixclaims.Status              `json:"status"`
		Verified            bool                          `json:"verified"`
		PairOutcome         fixclaims.PairOutcome         `json:"pairOutcome"`
		Package             string                        `json:"package"`
		ClaimedBadVersion   string                        `json:"claimedBadVersion"`
		ClaimedFixedVersion string                        `json:"claimedFixedVersion"`
		BadVersion          string                        `json:"badVersion"`
		GoodVersion         string                        `json:"goodVersion"`
		Environments        []fixclaims.EnvironmentResult `json:"environments"`
		FailureFingerprint  string                        `json:"failureFingerprint"`
		SampleID            string                        `json:"sampleId"`
		Evidence            []string                      `json:"evidence"`
		Upstream            map[string]any                `json:"upstream"`
		Reproducer          map[string]any                `json:"reproducer"`
		RunCount            int                           `json:"runCount"`
		Attempts            int                           `json:"attempts"`
		Closed              any                           `json:"closed"`
		CloseReason         string                        `json:"closeReason"`
		Runs                []fixclaims.Run               `json:"runs"`
	}
	var records []record
	byStatus := map[fixclaims.Status]int{}
	byOutcome := map[fixclaims.PairOutcome]int{}
	var farmSeconds, verifiedFarmSeconds int64
	for _, id := range ids {
		code, body, ok := fixClaimsCall(ctx, http.MethodGet, srv.URL, "", fmt.Sprintf("/v1/fix-claims/%d", id), nil)
		if !ok || code != http.StatusOK {
			t.Fatalf("record %d: %d %v", id, code, body)
		}
		raw, _ := json.Marshal(body)
		var rec record
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("record %d: %v", id, err)
		}
		records = append(records, rec)
		byStatus[rec.Status]++
		byOutcome[rec.PairOutcome]++
		for _, run := range rec.Runs {
			farmSeconds += run.FarmSeconds
			if rec.Verified {
				verifiedFarmSeconds += run.FarmSeconds
			}
		}

		// Integrity: nothing left CLAIMED_FIX without runs, and nothing is
		// verified without both halves of the pair under receipts.
		if rec.Status == fixclaims.StatusClaimedFix {
			t.Errorf("%s %s->%s (id %d) is still CLAIMED_FIX after the loop drained: runs=%d attempts=%d closeReason=%q",
				rec.Package, rec.ClaimedBadVersion, rec.ClaimedFixedVersion, id, rec.RunCount, rec.Attempts, rec.CloseReason)
		}
		if rec.Verified {
			var failReceipt, passReceipt bool
			for _, env := range rec.Environments {
				if env.BadRun != nil && env.BadRun.Verdict == fixclaims.VerdictFail && env.BadRun.ReceiptID != "" {
					failReceipt = true
				}
				if env.FixedRun != nil && env.FixedRun.Verdict == fixclaims.VerdictPass && env.FixedRun.ReceiptID != "" {
					passReceipt = true
				}
			}
			if !failReceipt || !passReceipt || rec.FailureFingerprint == "" || len(rec.Evidence) < 2 || rec.SampleID == "" {
				t.Errorf("%s (id %d) is %s without a FAIL and a PASS receipt: %+v", rec.Package, id, rec.Status, rec)
			}
			// Every receipt the record cites must be one this server holds,
			// including the boundary probes, whose repinned samples differ
			// from the pair's.
			held := map[string]bool{}
			for _, run := range rec.Runs {
				rows, err := store.ReceiptsForSample(ctx, run.SampleID)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					held[row.ReceiptID] = true
				}
			}
			for _, receiptID := range rec.Evidence {
				if !held[receiptID] {
					t.Errorf("%s (id %d) cites receipt %s the server does not hold", rec.Package, id, receiptID)
				}
			}
			if rec.Upstream["url"] == "" {
				t.Errorf("%s (id %d) verified without upstream provenance", rec.Package, id)
			}
		}
		if rec.Status == fixclaims.StatusClaimNotReproduced && (rec.Verified || rec.PairOutcome != fixclaims.PairNotReproduced) {
			t.Errorf("%s (id %d) NOT_REPRODUCED presented as %v / %s", rec.Package, id, rec.Verified, rec.PairOutcome)
		}
	}
	if byStatus[fixclaims.StatusVerifiedFix]+byStatus[fixclaims.StatusPartialFix] < 3 {
		t.Errorf("only %d verified fix records; the Phase 0 bar is several", byStatus[fixclaims.StatusVerifiedFix]+byStatus[fixclaims.StatusPartialFix])
	}

	code, metrics, ok := fixClaimsCall(ctx, http.MethodGet, srv.URL, token, "/v1/fix-claims/metrics", nil)
	if !ok || code != http.StatusOK {
		t.Fatalf("metrics: %d %v", code, metrics)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	summary := map[string]any{
		"schemaVersion": 1,
		"issue":         444,
		"measuredAt":    time.Now().UTC().Format(time.RFC3339),
		"host":          map[string]any{"os": "windows", "sandbox": string(domain.CapContainerRun), "probeOS": "linux"},
		"reproducers":   len(repros),
		"candidates":    len(candidates),
		"byStatus":      byStatus,
		"byPairOutcome": byOutcome,
		"farmSeconds":   farmSeconds,
		"farmSecondsPerVerifiedFix": func() float64 {
			n := byStatus[fixclaims.StatusVerifiedFix] + byStatus[fixclaims.StatusPartialFix]
			if n == 0 {
				return 0
			}
			return float64(verifiedFarmSeconds) / float64(n)
		}(),
		"wallSeconds": int64(wall.Seconds()),
		"metrics":     metrics,
		"records":     records,
	}
	t.Logf("phase 0: %d candidates, status %v, pair outcomes %v, %d farm seconds in %s", len(candidates), byStatus, byOutcome, farmSeconds, wall.Round(time.Second))
	if path := os.Getenv("CSX_FIX_PHASE0_EVIDENCE"); path != "" {
		encoded, _ := json.MarshalIndent(summary, "", "  ")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
