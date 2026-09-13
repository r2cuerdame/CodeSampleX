package localdb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyOpenCannotCreateOrWriteAStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	if db, err := OpenReadOnly(t.Context(), path); err == nil {
		db.Close()
		t.Fatal("read-only open created a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing database changed: %v", err)
	}
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.SetStat(t.Context(), "sentinel", "committed"); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if value, found, err := reader.GetStat(t.Context(), "sentinel"); err != nil || !found || value != "committed" {
		t.Fatalf("committed state unavailable: value=%q found=%v err=%v", value, found, err)
	}
	if err := reader.SetStat(t.Context(), "sentinel", "changed"); err == nil {
		t.Fatal("read-only store accepted a write")
	}
}
