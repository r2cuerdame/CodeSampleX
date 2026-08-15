package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
)

// The one promise a content-addressed store makes is that the id IS the
// sha256 of the bytes it hands back. Put took a file already sitting at the
// digest path as proof the object was stored, without ever reading it — so
// a file truncated by a crash, half-written by an older build, or damaged
// on disk kept that address occupied forever: Put reported success, and
// every later Get served corrupt bytes under an id saying they were intact.
//
// Put holds a verified copy at that moment. It is the only chance anything
// has to notice.
func TestPutRepairsAnObjectThatDoesNotMatchItsAddress(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	const content = "the real bytes of a sample tarball"
	id, err := s.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}

	// Damage the stored object in place, as a bad disk or a killed process
	// would leave it.
	sum := sha256.Sum256([]byte(content))
	path := s.pathFor(hex.EncodeToString(sum[:]))
	if err := os.WriteFile(path, []byte("truncated"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Putting the same content again must not shrug and report success.
	id2, err := s.Put(strings.NewReader(content))
	if err != nil {
		t.Fatalf("re-Put: %v", err)
	}
	if id2 != id {
		t.Fatalf("id changed: %q then %q", id, id2)
	}

	rc, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("Get returned %q under an id that promises %q", got, content)
	}
}

// The ordinary duplicate — the object is there and correct — still short
// circuits without rewriting anything.
func TestPutOfAnIntactDuplicateKeepsTheSameObject(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const content = "unchanged"
	id, err := s.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	path := s.pathFor(hex.EncodeToString(sum[:]))
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Put(strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an intact object was rewritten")
	}
	rc, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
}
