package identity

import (
	"sync"
	"testing"
)

func TestZZScratchConcurrentFirstRun(t *testing.T) {
	home := t.TempDir()
	const n = 8
	ids := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id, err := LoadOrCreate(home)
			if err != nil {
				t.Errorf("LoadOrCreate: %v", err)
				return
			}
			ids[i] = id.PeerID()
		}(i)
	}
	close(start)
	wg.Wait()
	distinct := map[string]int{}
	for _, s := range ids {
		distinct[s]++
	}
	t.Logf("distinct peer identities from one home on first run: %d %v", len(distinct), distinct)
	if len(distinct) != 1 {
		t.Errorf("one home produced %d distinct identities", len(distinct))
	}
	// And which one survived on disk?
	final, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("on-disk identity afterwards: %s (was among the returned ones: %v)", final.PeerID(), distinct[final.PeerID()] > 0)
}
