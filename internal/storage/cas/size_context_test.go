package cas

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestTotalSizeContextPreservesExactCompletedAccounting(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	size, err := s.TotalSizeContext(t.Context())
	if err != nil || size != 0 {
		t.Fatalf("known empty: %d %v", size, err)
	}
	for _, v := range []string{strings.Repeat("a", 150), strings.Repeat("b", 200)} {
		if _, err := s.Put(strings.NewReader(v)); err != nil {
			t.Fatal(err)
		}
	}
	old, err := s.TotalSize()
	if err != nil {
		t.Fatal(err)
	}
	size, err = s.TotalSizeContext(t.Context())
	if err != nil || size != old || size != 350 {
		t.Fatalf("exact accounting changed: %d %d %v", old, size, err)
	}
}

type slowDirFS struct {
	base            fs.FS
	calls, maxBatch int
}
type slowDir struct {
	fs.ReadDirFile
	owner *slowDirFS
}

func (s *slowDirFS) Open(name string) (fs.File, error) {
	f, err := s.base.Open(name)
	if err != nil {
		return nil, err
	}
	if dir, ok := f.(fs.ReadDirFile); ok {
		return &slowDir{ReadDirFile: dir, owner: s}, nil
	}
	return f, nil
}
func (d *slowDir) ReadDir(n int) ([]fs.DirEntry, error) {
	d.owner.calls++
	if n > d.owner.maxBatch {
		d.owner.maxBatch = n
	}
	if n <= 0 {
		d.owner.maxBatch = 1 << 20
	}
	time.Sleep(10 * time.Millisecond)
	return d.ReadDirFile.ReadDir(n)
}
func TestTotalSizeContextStopsBetweenBoundedDirectoryReads(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2048; i++ {
		if err := os.WriteFile(filepath.Join(root, strings.Repeat("x", 8)+string(rune(0x4000+i))), []byte("object"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := &slowDirFS{base: os.DirFS(root)}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	size, err := totalSizeFS(ctx, store)
	if !errors.Is(err, context.DeadlineExceeded) || size != 0 {
		t.Fatalf("partial walk must be unavailable, got %d %v", size, err)
	}
	if store.calls > 1 || store.maxBatch > 256 {
		t.Fatalf("full directory scan continued after timeout: calls=%d batch=%d", store.calls, store.maxBatch)
	}
}

type failDirFS struct{ base fs.FS }

func (s failDirFS) Open(name string) (fs.File, error) {
	if name == "zz" {
		return nil, fs.ErrPermission
	}
	return s.base.Open(name)
}
func TestTotalSizeContextDiscardsBytesOnFilesystemFailure(t *testing.T) {
	store := failDirFS{base: fstest.MapFS{"aa/object": &fstest.MapFile{Data: []byte("already-counted")}, "zz/object": &fstest.MapFile{Data: []byte("not-readable")}}}
	size, err := totalSizeFS(t.Context(), store)
	if !errors.Is(err, fs.ErrPermission) || size != 0 {
		t.Fatalf("failure appeared as partial cache: %d %v", size, err)
	}
}
func TestTotalSizeContextRejectsInvalidRootWithoutReadingIt(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sha256"), []byte("invalid root"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Store{root: root}
	size, err := s.TotalSizeContext(t.Context())
	if err == nil || size != 0 {
		t.Fatalf("invalid root was reported as cache: %d %v", size, err)
	}
}
