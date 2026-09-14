package serverstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func reportQueueContract(t *testing.T, store interface {
	CSXIssueStore
	AnomalyStore
	ReportQueueStore
}) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	var first int64
	for i := 0; i < 31; i++ {
		row, _, err := store.RecordCSXIssueReport(ctx, CSXIssueReportRow{Fingerprint: fmt.Sprintf("queue-%d", i), Component: fmt.Sprintf("component-%02d", i), ReportJSON: `{"actualBehavior":"private evidence"}`, Status: "no-replay-lane", ReporterBucket: "do-not-expose"}, now.AddDate(0, 0, -90).Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = row.ID
		}
	}
	_, _, err := store.RecordAnomalyReport(ctx, AnomalyReportRow{Fingerprint: "queue-anomaly", ReportJSON: `{}`, Status: "unsupported", PURL: "pkg:npm/example@1.0.0"}, now)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ReportQueue(ctx, ReportFilter{Channel: "product", State: "open", Limit: 25})
	if err != nil || page.Total != 31 || len(page.Rows) != 25 || page.Counts.Open != 31 || page.Counts.Blocked != 31 {
		t.Fatalf("all-time queue: %+v %v", page, err)
	}
	page, err = store.ReportQueue(ctx, ReportFilter{Channel: "product", State: "open", Limit: 25, Offset: 25})
	if err != nil || len(page.Rows) != 6 || page.Rows[5].ID != first {
		t.Fatalf("second page: %+v %v", page, err)
	}
	page, err = store.ReportQueue(ctx, ReportFilter{Channel: "all", State: "blocked", Query: "COMPONENT-00"})
	if err != nil || page.Total != 1 || page.Counts.Total != 32 {
		t.Fatalf("search: %+v %v", page, err)
	}
	for _, query := range []string{"%", "_", "' OR true --"} {
		page, err = store.ReportQueue(ctx, ReportFilter{Channel: "all", State: "all", Query: query})
		if err != nil || page.Total != 0 {
			t.Fatalf("literal search %q: %+v %v", query, page, err)
		}
	}
	note := strings.Repeat("measured evidence ", 180)
	ok, err := store.ReviewProductReport(ctx, first, "confirmed-csx-defect", note, "#386", now)
	if err != nil || !ok {
		t.Fatalf("review %v %v", ok, err)
	}
	ok, err = store.ReviewProductReport(ctx, first, "expected-behavior", "overwrite", "", now)
	if err != nil || ok {
		t.Fatalf("overwritten %v %v", ok, err)
	}
	got, err := store.ProductReviewNote(ctx, first)
	if err != nil || got != note {
		t.Fatalf("note %q %v", got, err)
	}
	if ok, err := store.LinkCSXIssueCanonical(ctx, first, "#other"); err != nil || ok {
		t.Fatalf("canonical reference overwritten %v %v", ok, err)
	}
	page, err = store.ReportQueue(ctx, ReportFilter{Channel: "product", State: "resolved"})
	if err != nil || page.Total != 1 || page.Counts.Open != 30 || page.Rows[0].CanonicalRef != "#386" {
		t.Fatalf("after review %+v %v", page, err)
	}
	page, err = store.ReportQueue(ctx, ReportFilter{Channel: "product", State: "all", Offset: 500})
	if err != nil || page.Total != 31 || len(page.Rows) != 0 {
		t.Fatalf("empty page lost counts %+v %v", page, err)
	}
}
func TestFakeReportQueue(t *testing.T)          { reportQueueContract(t, NewFake()) }
func TestIntegrationPGReportQueue(t *testing.T) { reportQueueContract(t, openTestPG(t)) }
