package identity

import (
	"sync"
	"testing"
)

// os.WriteFile truncates, so every process that started at the same moment
// and found no identity generated one and wrote over the others: each
// caller then kept using the key it had made in memory, which was not the
// one on disk.
//
// The daemon, the MCP server and a CLI command all start together on a
// first run, so this is the ordinary path. The anonSeed is what every
// rotating evidence ID derives from, so disagreeing about it makes one
// machine count as several independent peers — the exact inflation the
// server side was fixed to stop, arriving from the client instead.
func TestConcurrentFirstRunAgreesOnOneIdentity(t *testing.T) {
	home := t.TempDir()
	const n = 8

	ids := make([]string, n)
	anon := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, err := LoadOrCreate(home)
			if err != nil {
				t.Errorf("LoadOrCreate: %v", err)
				return
			}
			ids[i] = id.PeerID()
			anon[i] = id.AnonID("2026-08-15")
		}()
	}
	close(start)
	wg.Wait()

	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("concurrent first run produced %d different peer IDs (%s vs %s)",
				len(uniq(ids)), ids[0], ids[i])
		}
		if anon[i] != anon[0] {
			t.Fatalf("concurrent first run produced different anon IDs: %s vs %s", anon[0], anon[i])
		}
	}

	// And the identity everyone holds is the one on disk.
	onDisk, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.PeerID() != ids[0] {
		t.Errorf("the persisted identity %s is not the one in use %s", onDisk.PeerID(), ids[0])
	}
}

func uniq(in []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range in {
		m[s] = true
	}
	return m
}
