package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recovered(failedVersion, reason, ran string) Resolution {
	return Resolution{
		Descriptor:    Descriptor{Version: ran, SHA256: strings.Repeat("a", 64), Sequence: 5},
		Recovered:     true,
		FailedVersion: failedVersion,
		FailedReason:  reason,
		Healed:        true,
	}
}

// A run that never recovered must leave nothing behind. An empty record would
// read as "a recovery happened and we lost the detail".
func TestNoRecoveryRecordsNothing(t *testing.T) {
	root := t.TempDir()
	if err := RecordRecovery(root, Resolution{Descriptor: Descriptor{Version: "v1.0.0"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(RecoveryRecordPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a healthy run wrote a recovery record (%v)", err)
	}
	rec, ok, err := ReadRecoveryRecord(root)
	if err != nil || ok {
		t.Fatalf("ReadRecoveryRecord = %+v, %t, %v; want absent", rec, ok, err)
	}
}

// The whole point of the file: after the pointer is repaired, no later run
// says anything, so the record is the only place the incident still exists.
func TestRecoveryRecordKeepsWhatTheStderrLineSaidOnce(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 24, 8, 7, 14, 0, time.UTC)
	if err := RecordRecovery(root, recovered("v0.1.44", ReasonPayloadMissing, "v0.1.22"), at); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := ReadRecoveryRecord(root)
	if err != nil || !ok {
		t.Fatalf("ReadRecoveryRecord = %+v, %t, %v", rec, ok, err)
	}
	if rec.FailedVersion != "v0.1.44" || rec.FailedReason != ReasonPayloadMissing || rec.RanVersion != "v0.1.22" {
		t.Fatalf("record = %+v", rec)
	}
	if !rec.PointerRepaired || rec.PointerError != "" {
		t.Fatalf("repaired pointer recorded as %+v", rec)
	}
	if rec.Observations != 1 || !rec.FirstObservedAt.Equal(at) || !rec.LastObservedAt.Equal(at) {
		t.Fatalf("timing = %+v", rec)
	}
	if s := rec.Summary(); !strings.Contains(s, "v0.1.44") || !strings.Contains(s, ReasonPayloadMissing) || !strings.Contains(s, "v0.1.22") {
		t.Fatalf("Summary() = %q", s)
	}
}

// Defender has quarantined this project's payload on four separate days. One
// incident that keeps repeating and four unrelated ones are different
// operational facts, so the record counts the repeats and dates the first.
func TestRepeatedSameFailureAccumulatesAndKeepsTheFirstSighting(t *testing.T) {
	root := t.TempDir()
	first := time.Date(2026, 8, 24, 8, 7, 14, 0, time.UTC)
	for i := range 3 {
		at := first.Add(time.Duration(i) * 24 * time.Hour)
		if err := RecordRecovery(root, recovered("v0.1.44", ReasonPayloadMissing, "v0.1.22"), at); err != nil {
			t.Fatal(err)
		}
	}
	rec, _, err := ReadRecoveryRecord(root)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Observations != 3 {
		t.Fatalf("observations = %d, want 3", rec.Observations)
	}
	if !rec.FirstObservedAt.Equal(first) {
		t.Fatalf("firstObservedAt = %s, want %s", rec.FirstObservedAt, first)
	}
	if !rec.LastObservedAt.Equal(first.Add(48 * time.Hour)) {
		t.Fatalf("lastObservedAt = %s", rec.LastObservedAt)
	}
	if s := rec.Summary(); !strings.Contains(s, "seen 3 times") {
		t.Fatalf("Summary() = %q", s)
	}
}

// A different payload failing is a new incident, not the old one continuing.
// Carrying the count over would make one bad release look like a machine that
// has been broken for a week.
func TestADifferentFailureStartsANewIncident(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 24, 8, 7, 14, 0, time.UTC)
	if err := RecordRecovery(root, recovered("v0.1.44", ReasonPayloadMissing, "v0.1.22"), at); err != nil {
		t.Fatal(err)
	}
	later := at.Add(72 * time.Hour)
	if err := RecordRecovery(root, recovered("v0.1.45", ReasonPayloadCorrupt, "v0.1.22"), later); err != nil {
		t.Fatal(err)
	}
	rec, _, err := ReadRecoveryRecord(root)
	if err != nil {
		t.Fatal(err)
	}
	if rec.FailedVersion != "v0.1.45" || rec.FailedReason != ReasonPayloadCorrupt {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Observations != 1 || !rec.FirstObservedAt.Equal(later) {
		t.Fatalf("new incident inherited the old one: %+v", rec)
	}
}

// An install that recovers every single run without ever repairing its pointer
// is a different fault from one that recovered once and healed. The record has
// to be able to tell them apart.
func TestAnUnrepairedPointerIsRecordedAndSaidOutLoud(t *testing.T) {
	root := t.TempDir()
	res := recovered("v0.1.44", ReasonPayloadMissing, "v0.1.22")
	res.Healed = false
	res.HealError = errors.New("install update lock is busy")
	if err := RecordRecovery(root, res, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec, _, err := ReadRecoveryRecord(root)
	if err != nil {
		t.Fatal(err)
	}
	if rec.PointerRepaired {
		t.Fatal("an unhealed pointer was recorded as repaired")
	}
	if !strings.Contains(rec.PointerError, "lock is busy") {
		t.Fatalf("pointerError = %q", rec.PointerError)
	}
	if s := rec.Summary(); !strings.Contains(s, "NOT repaired") {
		t.Fatalf("Summary() = %q", s)
	}
}

// A record that cannot be parsed is evidence that something happened, and
// reporting it as "no recovery has ever occurred" would erase exactly the fact
// the file exists to keep.
func TestUnreadableRecordIsAnErrorNotAnAbsence(t *testing.T) {
	for name, body := range map[string]string{
		"not json":        "{not json",
		"wrong schema":    `{"schema":99,"failedVersion":"v1.0.0","failedReason":"payload-missing","ranVersion":"v0.9.0"}`,
		"missing subject": `{"schema":1,"failedReason":"payload-missing"}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(RecoveryRecordPath(root), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			rec, ok, err := ReadRecoveryRecord(root)
			if err == nil {
				t.Fatalf("ReadRecoveryRecord = %+v, %t; want an error", rec, ok)
			}
			if ok {
				t.Fatal("an unreadable record reported itself as present and usable")
			}
		})
	}
}

// The record lives beside active.json, and active.json refuses unknown fields.
// Keeping them in separate files is what makes this evidence addable without
// touching the pointer the launcher's safety depends on.
func TestRecoveryRecordDoesNotDisturbTheActivePointer(t *testing.T) {
	root := t.TempDir()
	payload, err := PayloadPath(root, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("csx test fixture payload: installed version")
	if err := os.WriteFile(payload, body, 0o700); err != nil {
		t.Fatal(err)
	}
	sum, err := fileHash(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := Active{Schema: Schema, Current: Descriptor{Version: "v1.0.0", SHA256: sum, Sequence: 3}}
	if err := Write(root, want); err != nil {
		t.Fatal(err)
	}
	if err := RecordRecovery(root, recovered("v1.1.0", ReasonPayloadMissing, "v1.0.0"), time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatalf("the recovery record broke the pointer: %v", err)
	}
	if got.Current != want.Current {
		t.Fatalf("current = %+v, want %+v", got.Current, want.Current)
	}
}
