package cas

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestPutGetRoundTrip(t *testing.T) {
	s := newStore(t)
	content := []byte("hello content-addressed world")

	id, err := s.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if want := domain.SHA256Hex(content); id != want {
		t.Fatalf("Put id = %q, want %q", id, want)
	}

	rc, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("Get content = %q, want %q", got, content)
	}
}

func TestPutIdempotent(t *testing.T) {
	s := newStore(t)
	id1, err := s.Put(strings.NewReader("same bytes"))
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	id2, err := s.Put(strings.NewReader("same bytes"))
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %q vs %q", id1, id2)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
}

func TestHasAndDelete(t *testing.T) {
	s := newStore(t)
	id, err := s.Put(strings.NewReader("to be deleted"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !s.Has(id) {
		t.Fatalf("Has(%q) = false after Put", id)
	}
	if err := s.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.Has(id) {
		t.Fatalf("Has(%q) = true after Delete", id)
	}
	if _, err := s.Get(id); !os.IsNotExist(err) {
		t.Fatalf("Get after Delete: err = %v, want IsNotExist", err)
	}
}

func TestDeleteMissing(t *testing.T) {
	s := newStore(t)
	err := s.Delete("sha256:" + strings.Repeat("ab", 32))
	if !os.IsNotExist(err) {
		t.Fatalf("Delete missing: err = %v, want IsNotExist", err)
	}
}

func TestTotalSize(t *testing.T) {
	s := newStore(t)
	total, err := s.TotalSize()
	if err != nil {
		t.Fatalf("TotalSize empty: %v", err)
	}
	if total != 0 {
		t.Fatalf("TotalSize empty = %d, want 0", total)
	}
	if _, err := s.Put(bytes.NewReader(make([]byte, 100))); err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	if _, err := s.Put(bytes.NewReader(bytes.Repeat([]byte{1}, 250))); err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	total, err = s.TotalSize()
	if err != nil {
		t.Fatalf("TotalSize: %v", err)
	}
	if total != 350 {
		t.Fatalf("TotalSize = %d, want 350", total)
	}
}

func TestList(t *testing.T) {
	s := newStore(t)
	id1, _ := s.Put(strings.NewReader("one"))
	id2, _ := s.Put(strings.NewReader("two"))
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	seen := map[string]bool{list[0]: true, list[1]: true}
	if !seen[id1] || !seen[id2] {
		t.Fatalf("List = %v, want %q and %q", list, id1, id2)
	}
}

func TestMalformedIDRejected(t *testing.T) {
	s := newStore(t)
	bad := []string{
		"",
		"sha256:",
		"sha256:short",
		"md5:" + strings.Repeat("ab", 32),
		strings.Repeat("ab", 32),             // missing prefix
		"sha256:" + strings.Repeat("AB", 32), // uppercase hex
		"sha256:" + strings.Repeat("zz", 32), // non-hex
		"sha256:" + strings.Repeat("ab", 32) + "cd",  // too long
		"sha256:../../" + strings.Repeat("ab", 28),   // traversal attempt
		"sha256:aa/bb/" + strings.Repeat("cd", 29),   // separator smuggling
		"sha256:aa\\bb\\" + strings.Repeat("cd", 29), // windows separator
	}
	for _, id := range bad {
		if s.Has(id) {
			t.Errorf("Has(%q) = true, want false", id)
		}
		if _, err := s.Get(id); err == nil {
			t.Errorf("Get(%q): nil error, want invalid-id error", id)
		}
		if err := s.Delete(id); err == nil {
			t.Errorf("Delete(%q): nil error, want invalid-id error", id)
		}
	}
}

func TestNoTempLeftovers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cas")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Put(strings.NewReader("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.Contains(d.Name(), "tmp") {
			t.Errorf("temp file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
