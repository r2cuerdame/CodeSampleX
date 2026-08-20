package identity

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The window this covers is microseconds wide and a Windows laptop never hits
// it, so the concurrency test that found it in Linux CI passed 200 times
// locally. This reproduces the same state directly: the file EXISTS and is
// not finished, which is exactly what O_EXCL leaves behind between creating
// the file and writing into it.
//
// LoadOrCreate used to answer that with a bare read and fail on "unexpected
// end of JSON input" — returning no identity at all, so the caller minted its
// own and one machine began counting as several independent peers.
func TestIdentityWaitsForAFileThatExistsButIsNotFinished(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "identity.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// The writer finishes a moment later, as the O_EXCL winner does.
	seedHome := t.TempDir()
	if _, err := LoadOrCreate(seedHome); err != nil {
		t.Fatal(err)
	}
	finished, err := os.ReadFile(filepath.Join(seedHome, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		time.Sleep(6 * time.Millisecond)
		done <- os.WriteFile(path, finished, 0o600)
	}()

	id, err := LoadOrCreate(home)
	if werr := <-done; werr != nil {
		t.Fatalf("writer: %v", werr)
	}
	if err != nil {
		t.Fatalf("LoadOrCreate gave up on an unfinished file: %v", err)
	}
	if id == nil || id.PeerID() == "" {
		t.Fatal("no identity returned for a file that was completed while waiting")
	}
}
