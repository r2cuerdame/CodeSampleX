package verifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A cross-verification that fails at resolve leaves nothing behind: the
// receipt keeps a digest, the workspace is disposable, and the logs were
// dropped on the floor between RunLogged and Run. Two peers hit the same
// Django 6.1 resolve failure a day apart and neither run could be diagnosed,
// because the difference between "works by hand" and "fails in the verifier"
// is exactly what those logs hold.
func TestStageLogsAreKeptOnlyWhenSomethingFailed(t *testing.T) {
	home := t.TempDir()
	store := &StageLogStore{Home: home, Now: func() time.Time {
		return time.Date(2026, 8, 20, 9, 24, 0, 0, time.UTC)
	}}

	// Everything passed: nothing to diagnose, nothing written.
	path, err := store.Keep("sha256:allgood", map[string]string{"resolve": "ok"},
		map[string]string{"resolve": "PASS", "compile": "PASS"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Errorf("a clean run left a log at %q", path)
	}

	// A failure: the run is kept, named so it can be found again.
	path, err = store.Keep("sha256:deadbeefcafe",
		map[string]string{"resolve": "ERROR: No matching distribution"},
		map[string]string{"resolve": "FAIL", "compile": "SKIPPED"})
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("a failed run left nothing to diagnose")
	}
	base := filepath.Base(path)
	if !strings.Contains(base, "deadbeefcafe") {
		t.Errorf("file name %q does not carry the sample id", base)
	}
	if !strings.Contains(base, "20260820") {
		t.Errorf("file name %q does not carry a timestamp", base)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"resolve", "FAIL", "No matching distribution"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("kept log is missing %q:\n%s", want, body)
		}
	}
}

// Logs are diagnosis, not a precondition of verification. A home that cannot
// be written must not fail the run that produced the evidence.
func TestKeepingLogsNeverFailsTheVerification(t *testing.T) {
	store := &StageLogStore{Home: filepath.Join(t.TempDir(), "nope", "\x00bad")}
	path, err := store.Keep("sha256:x", map[string]string{"resolve": "boom"},
		map[string]string{"resolve": "FAIL"})
	if err == nil && path == "" {
		return // refused quietly, which is also fine
	}
	if err != nil && path != "" {
		t.Errorf("reported both a path %q and an error %v", path, err)
	}
}

// Unbounded diagnostics fill a disk on a machine nobody is watching.
func TestStageLogsAreCapped(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	store := &StageLogStore{Home: home, MaxFiles: 3, Now: func() time.Time { return now }}

	var kept []string
	for i := range 6 {
		now = now.Add(time.Minute)
		p, err := store.Keep("sha256:run"+string(rune('a'+i)),
			map[string]string{"resolve": "boom"}, map[string]string{"resolve": "FAIL"})
		if err != nil {
			t.Fatal(err)
		}
		kept = append(kept, p)
	}
	entries, err := os.ReadDir(filepath.Dir(kept[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("kept %d logs, want the cap of 3", len(entries))
	}
	// The newest survive; the oldest are the ones dropped.
	for _, p := range kept[3:] {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("a recent log was evicted: %v", err)
		}
	}
}
