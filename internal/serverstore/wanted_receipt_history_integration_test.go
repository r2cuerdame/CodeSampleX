package serverstore

import (
	"fmt"
	"testing"
	"time"
)

// Repeated verification is additional evidence; it cannot revoke an older
// exact version/platform answer merely by pushing it outside a newest-N window.
func TestIntegrationWantedRetainsOlderPlatformAndResolvedVersionAnswers(t *testing.T) {
	pg := openTestPG(t)
	ctx := t.Context()
	const sampleID = "sha256:wanted-history"
	if err := pg.SaveSample(ctx, SampleRow{SampleID: sampleID, ManifestJSON: `{"packages":["pkg:npm/history-fixture@0.1.0"],"symbols":["run"]}`}); err != nil {
		t.Fatal(err)
	}
	requests := []WantedRow{
		{Ecosystem: "npm", Name: "history-fixture", Version: "1.0.0", Symbol: "run", TargetOS: "windows"},
		{Ecosystem: "npm", Name: "history-fixture", Version: "1.0.0", Symbol: "run", TargetOS: "linux"},
		{Ecosystem: "npm", Name: "history-fixture", Version: "2.0.0", Symbol: "run", TargetOS: "linux"},
		{Ecosystem: "npm", Name: "history-fixture", Version: "2.0.0", Symbol: "run", TargetOS: "windows"},
		{Ecosystem: "npm", Name: "history-fixture", Version: "3.0.0", Symbol: "run", TargetOS: "linux"},
		{Ecosystem: "npm", Name: "history-fixture", Version: "1.0.0", Symbol: "never-tested", TargetOS: "windows"},
	}
	if err := pg.RecordWanted(ctx, "2026-09-07", "history-reader", requests); err != nil {
		t.Fatal(err)
	}
	save := func(id, os, version string) {
		t.Helper()
		err := pg.SaveReceipt(ctx, ReceiptRow{ReceiptID: id, SampleID: sampleID, EnvHash: "env-" + os, ContractResult: "PASS",
			ReceiptJSON: fmt.Sprintf(`{"schemaVersion":2,"environment":{"os":%q},"stages":{"resolve":"PASS","contract":"PASS"},"resolvedPackages":["pkg:npm/history-fixture@%s"]}`, os, version)})
		if err != nil {
			t.Fatal(err)
		}
	}
	save("old-windows", "windows", "1.0.0")
	save("old-linux", "linux", "1.0.0")
	// Same environment hash, newer resolved version: DISTINCT ON(env_hash)
	// would also erase the older version answer, even without LIMIT 10.
	for i := 0; i < 12; i++ {
		save(fmt.Sprintf("new-linux-%02d", i), "linux", "2.0.0")
	}
	start := time.Now()
	rows, err := pg.TopWanted(ctx, 200)
	t.Logf("TopWanted(200) fixture elapsed=%s remaining=%d", time.Since(start), len(rows))
	if err != nil {
		t.Fatal(err)
	}
	remaining := map[string]bool{}
	for _, row := range rows {
		remaining[row.Version+"/"+row.Symbol+"/"+row.TargetOS] = true
	}
	for _, key := range []string{"1.0.0/run/windows", "1.0.0/run/linux", "2.0.0/run/linux"} {
		if remaining[key] {
			t.Errorf("older verified answer was masked by unrelated reruns: %s", key)
		}
	}
	for _, key := range []string{"2.0.0/run/windows", "3.0.0/run/linux", "1.0.0/never-tested/windows"} {
		if !remaining[key] {
			t.Errorf("unverified coordinate was incorrectly closed: %s", key)
		}
	}
	if len(rows) != 3 {
		t.Errorf("remaining rows=%d, want exactly 3 genuinely unanswered coordinates", len(rows))
	}
	if loops := listWantedExpansionLoops(t, pg); loops != 0 {
		t.Fatalf("wanted expanded manifest package arrays %.0f times", loops)
	}
}
