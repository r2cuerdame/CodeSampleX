package main

import (
	"os"
	"strings"
	"testing"
)

// Boot must enforce the dedup retention window.
//
// PurgeDedupOlderThan has existed, documented, with a stated 30-day window
// (goal.md 14.4) and no caller. docs/data-rights.md says so outright: "no
// non-test caller that schedules that method and no deployment step that
// deletes from evidence_dedup". So the retention policy this project
// documents to its contributors was not being applied to their data.
//
// Measured on production 2026-09-01: evidence_dedup held 553,823 rows across
// 21 epochs, the oldest 0001-01-01. Only 1,102 of them are past the window
// today, so this is not an answer to the database pressure measured the same
// hour -- it is a commitment being kept. It becomes load-bearing as the
// corpus ages past thirty days, which is soon.
func TestBootPurgesDedupPastTheRetentionWindow(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "PurgeDedupOlderThan") {
		t.Fatal("boot never applies the dedup retention window; the policy is documented and unenforced")
	}
	if !strings.Contains(body, "dedupRetentionDays") {
		t.Error("the retention window is not a named constant")
	}
	// It runs beside the other boot reconciles, not on a request path: this
	// is a finite sweep with a good enough clock, and putting it in front of
	// a page would put a DELETE on the hot path of a 2GB host.
	at := strings.Index(body, "PurgeDedupOlderThan")
	lanes := strings.Index(body, "ReconcileCrossJobLanes")
	if lanes < 0 || at < lanes {
		t.Error("the purge does not run with the other boot reconciles")
	}
}

// The window is the documented one. A number that drifts from the document
// is a retention promise nobody can check.
func TestTheRetentionWindowIsThirtyDays(t *testing.T) {
	if dedupRetentionDays != 30 {
		t.Errorf("dedupRetentionDays = %d, want the documented 30 (goal.md 14.4)", dedupRetentionDays)
	}
}
