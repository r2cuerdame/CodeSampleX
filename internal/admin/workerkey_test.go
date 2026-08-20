package admin

import (
	"strings"
	"testing"
)

// The worker key becomes a directory name inside a generated .bat and .sh, so
// it is derived rather than taken. cleanAuthoringLabel only rejects newlines
// and NUL — a label is otherwise free text, and "..\..\Windows" or
// `a" & del /q * & set "x=` would be interpolated straight into the script.
func TestWorkerKeyIsPathSafe(t *testing.T) {
	for _, label := range []string{
		`..\..\Windows\System32`,
		`../../etc`,
		`a" & del /q * & set "x=`,
		`worker $(rm -rf ~)`,
		`%LOCALAPPDATA%`,
		`  spaces and 'quotes'  `,
		`한글-워커`,
	} {
		key := authoringWorkerKey(label)
		if key == "" {
			t.Errorf("label %q produced an empty key", label)
		}
		if strings.ContainsAny(key, `/\:*?"<>|$%&' ()`) {
			t.Errorf("label %q produced an unsafe key %q", label, key)
		}
		if strings.Contains(key, "..") {
			t.Errorf("label %q produced a traversing key %q", label, key)
		}
	}
}

// A worker's home must be the SAME across its sessions — that is the whole
// point of keying on the worker rather than the session — and different
// between workers, so several can run on one machine at once.
func TestWorkerKeyIsStablePerWorkerAndDistinctBetweenThem(t *testing.T) {
	if a, b := authoringWorkerKey("laptop-01"), authoringWorkerKey("laptop-01"); a != b {
		t.Errorf("same label gave %q then %q", a, b)
	}
	if a, b := authoringWorkerKey("laptop-01"), authoringWorkerKey("laptop-02"); a == b {
		t.Errorf("two workers share the key %q", a)
	}
	// Slugging must not collide two labels that differ only in stripped
	// characters, or two workers would share an identity.
	if a, b := authoringWorkerKey("a/b"), authoringWorkerKey("a\b"); a == b {
		t.Errorf("distinct labels collided on %q", a)
	}
}
