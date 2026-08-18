package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func payloadFixture(t *testing.T, root, version, body string, sequence uint64) Descriptor {
	t.Helper()
	path, err := PayloadPath(root, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return Descriptor{Version: version, SHA256: hex.EncodeToString(sum[:]), Sequence: sequence}
}

func TestActivePointerRejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	root := t.TempDir()
	d := payloadFixture(t, root, "v1.0.0", "first", 1)
	for name, raw := range map[string]string{
		"unknown":          `{"schema":1,"current":{"version":"v1.0.0","sha256":"` + d.SHA256 + `","sequence":1},"extra":true}`,
		"duplicate":        `{"schema":1,"schema":1,"current":{"version":"v1.0.0","sha256":"` + d.SHA256 + `","sequence":1}}`,
		"nested duplicate": `{"schema":1,"current":{"version":"v1.0.0","version":"v1.0.0","sha256":"` + d.SHA256 + `","sequence":1}}`,
		"trailing":         `{"schema":1,"current":{"version":"v1.0.0","sha256":"` + d.SHA256 + `","sequence":1}} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(Path(root), []byte(strings.TrimSpace(raw)), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(root); err == nil {
				t.Fatal("invalid pointer accepted")
			}
		})
	}
}

func TestActivePointerCommitAndRollback(t *testing.T) {
	root := t.TempDir()
	first := payloadFixture(t, root, "v1.0.0", "first", 1)
	if err := Write(root, Active{Schema: 1, Current: first}); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, "new.exe")
	if err := os.WriteFile(staged, []byte("second"), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("second"))
	second := Descriptor{Version: "v1.1.0", SHA256: hex.EncodeToString(sum[:]), Sequence: 2}
	got, err := CommitPayload(root, staged, second)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current.Version != "v1.1.0" || got.Previous == nil || got.Previous.Version != "v1.0.0" {
		t.Fatalf("active=%+v", got)
	}
	got, err = Rollback(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current.Version != "v1.0.0" || got.Current.Sequence != 1 || got.Previous.Version != "v1.1.0" {
		t.Fatalf("rollback=%+v", got)
	}
}

func TestDescriptorSequencePromotionPreservesPreviousAndHold(t *testing.T) {
	root := t.TempDir()
	previous := payloadFixture(t, root, "v1.0.0", "old", 7)
	current := payloadFixture(t, root, "v1.1.0", "new", 8)
	hold := current
	if err := Write(root, Active{Schema: 1, Current: current, Previous: &previous, RollbackHold: &hold}); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, "unused.exe")
	if err := os.WriteFile(staged, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	current.Sequence = 20
	got, err := CommitPayload(root, staged, current)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current.Sequence != 20 || got.Previous == nil || got.Previous.Version != "v1.0.0" || got.RollbackHold == nil {
		t.Fatalf("promotion lost pointer history: %+v", got)
	}
}

func TestRollbackIsIdempotentlyHeld(t *testing.T) {
	root := t.TempDir()
	old := payloadFixture(t, root, "v1.0.0", "old", 7)
	bad := payloadFixture(t, root, "v1.1.0", "bad", 8)
	if err := Write(root, Active{Schema: 1, Current: bad, Previous: &old}); err != nil {
		t.Fatal(err)
	}
	got, err := Rollback(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.RollbackHold == nil || got.RollbackHold.Sequence != 8 {
		t.Fatalf("rollback hold=%+v", got)
	}
	if _, err := Rollback(root); err == nil {
		t.Fatal("second rollback toggled back to rejected payload")
	}
}

func TestActivePointerRejectsCorruptAndNoncanonicalPayload(t *testing.T) {
	root := t.TempDir()
	if _, err := PayloadPath(root, "v01.0.0"); err == nil {
		t.Fatal("noncanonical version accepted")
	}
	d := payloadFixture(t, root, "v1.0.0", "first", 1)
	d.SHA256 = "00" + d.SHA256[2:]
	if err := Write(root, Active{Schema: 1, Current: d}); err == nil {
		t.Fatal("corrupt payload accepted")
	}
}
