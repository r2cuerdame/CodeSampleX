package daemon

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// TestUIRendersDashboard covers the §12.5 page: every dashboard section
// is present, the reasoning-avoided figure carries the Estimated label,
// and the privacy preview renders the pending payloads.
func TestUIRendersDashboard(t *testing.T) {
	home := newTestHome(t, nil)
	d, _ := startDaemon(t, home)
	ctx := context.Background()

	// Dashboard inputs: one adopted hit with a build-pass report, one
	// miss, one dependency, one pending observation.
	offerID, err := d.DB.RecordSearchOffer(ctx, localdb.HitRow{
		TS: time.Now(), Query: "q", Grade: domain.GradeCompatible,
		SampleID: "sha256:aaa1",
	}, localdb.InterventionRow{
		SampleID: "sha256:aaa1", ExactFailureMatched: true, VerifiedOffer: true,
	})
	if err != nil {
		t.Fatalf("record search offer: %v", err)
	}
	if _, err := d.DB.CorrelateInterventionAdoption(ctx, offerID, "sha256:aaa1", true,
		sql.NullBool{Bool: true, Valid: true}, ""); err != nil {
		t.Fatalf("correlate intervention: %v", err)
	}
	d.incrStat(ctx, statMisses, 1)
	purl := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	if err := d.DB.UpsertPackage(ctx, purl, "PUBLIC"); err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	env := testEnv()
	if err := d.DB.SaveEnvironment(ctx, env); err != nil {
		t.Fatalf("save env: %v", err)
	}
	if err := d.DB.RecordObservation(ctx, localdb.ObsKey{
		Epoch: "2026-08-13", PURL: purl.String(), EnvHash: env.Hash(),
		Stage: domain.StageProjectCompile, Result: domain.ResultPass,
	}, 1); err != nil {
		t.Fatalf("record observation: %v", err)
	}

	resp, err := http.Get(d.BaseURL() + "/ui")
	if err != nil {
		t.Fatalf("GET /ui: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)

	for _, want := range []string{
		"Community status",
		"LOCAL ONLY",
		"Local cache",
		"Project dependencies",
		"Hits / Misses",
		"Post-hit build pass",
		"Estimated reasoning avoided",
		"Estimated", // the mandatory label (§12.5, plan P5.5)
		"Automatic evidence sent",
		"Origin Seeds",
		"Cross verifications",
		"Verified failure detours",
		"Exact failure matches",
		"Verified detours offered",
		"Reported failures avoided",
		"1 PASS / 0 FAIL",
		"no time-saved estimate",
		"Privacy preview",
		"npm/axios",            // dependency table row
		"pkg:npm/axios@1.12.0", // preview shows the pending batch verbatim
		"observationCount",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/ui page missing %q", want)
		}
	}

	// The dashboard never leaks external asset references.
	for _, banned := range []string{"http://cdn", "https://cdn", "<script src=", `link rel="stylesheet" href="http`} {
		if strings.Contains(page, banned) {
			t.Errorf("/ui page references external asset: %q", banned)
		}
	}
}
