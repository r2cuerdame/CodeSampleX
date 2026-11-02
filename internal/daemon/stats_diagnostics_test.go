package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStatsDiagnosticsPreserveFactsAndSuppressPrivateMetadata(t *testing.T) {
	home := newTestHome(t, nil)
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Enqueue(t.Context(), "adoption", "private-payload-marker"); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.SetStat(t.Context(), statLastDeliveryAttempt, `{"attemptId":"req_1234567890abcdef12345678","requestId":"unknown","outcome":"private-secret-marker","httpStatus":202,"accepted":1,"rejected":0,"elapsedMillis":1,"recordedAt":"2026-10-13T17:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	plain, err := d.StatsNow(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ctx := WithStatsDiagnostics(t.Context(), &out)
	traced, err := d.StatsNow(ctx)
	FinishStatsDiagnostics(ctx, err == nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(plain)
	b, _ := json.Marshal(traced)
	if !bytes.Equal(a, b) {
		t.Fatalf("diagnostics changed facts %s %s", a, b)
	}
	got := out.String()
	if !strings.Contains(got, "stage=cas_size state=begin") || !strings.Contains(got, "evidence_oldest_seconds=unrecorded") || !strings.Contains(got, "report_oldest_seconds=") {
		t.Fatalf("missing actual stage/age: %s", got)
	}
	if strings.Contains(got, "private-payload-marker") || strings.Contains(got, "private-secret-marker") || strings.Contains(got, home) {
		t.Fatal("private content escaped")
	}
}
func TestStatsDiagnosticsCaptureFailedStageWithoutErrorBody(t *testing.T) {
	var out bytes.Buffer
	ctx := WithStatsDiagnostics(context.Background(), &out)
	_, err := StatsFromDisk(ctx, t.TempDir()+"/private-home-marker")
	FinishStatsDiagnostics(ctx, err == nil)
	if err == nil {
		t.Fatal("missing store unexpectedly opened")
	}
	got := out.String()
	if !strings.Contains(got, "stage=db_open state=failed") || strings.Contains(got, "private-home-marker") {
		t.Fatalf("unsafe/missing failure trace: %s", got)
	}
}
func TestStatsDiagnosticsBoundOutputAndIgnoreUnknownStages(t *testing.T) {
	var out bytes.Buffer
	ctx := WithStatsDiagnostics(t.Context(), &out)
	BeginStatsStage(ctx, "private-stage-marker")(nil)
	for i := 0; i < 1000; i++ {
		BeginStatsStage(ctx, "cas_size")(nil)
	}
	FinishStatsDiagnostics(ctx, true)
	got := out.String()
	if len(got) > 8192 || strings.Count(got, "\n") > 48 || strings.Contains(got, "private-stage-marker") {
		t.Fatal("trace bounds or vocabulary escaped")
	}
}
func TestStatsDiagnosticsDaemonFastPathReadsOnlyBoundedDiagnostics(t *testing.T) {
	home := newTestHome(t, nil)
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	var out bytes.Buffer
	ctx := WithStatsDiagnostics(t.Context(), &out)
	ReadStatsDiagnosticsFromDisk(ctx, home, 0)
	ReadStatsDiagnosticsFromDisk(ctx, home, 0)
	FinishStatsDiagnostics(ctx, true)
	got := out.String()
	if strings.Contains(got, "stage=cas_size") || strings.Count(got, "stage=delivery_receipt state=begin") != 1 {
		t.Fatalf("duplicate/heavy fallback diagnostics: %s", got)
	}
}

var _ = time.Second
