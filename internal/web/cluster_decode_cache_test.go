package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type farm202ClusterStore struct {
	*fakeStore
	raw []string
}

func (s *farm202ClusterStore) FailureClusters(context.Context, string, string) ([]string, int, error) {
	return append([]string(nil), s.raw...), len(s.raw), nil
}
func farm202ClusterRows(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("{\"fingerprint\":\"error-%d\",\"stage\":\"compile\",\"errorCode\":\"MISSING_API\",\"errorSummary\":\"complete observed failure\",\"versions\":[\"1.0.0\"],\"envSummary\":{\"os\":\"linux\",\"runtime\":\"node@22.1\"},\"count\":1}", i)
	}
	return out
}
func TestFarm202RepeatedClusterPageDoesNotDecodeAgain(t *testing.T) {
	store := &farm202ClusterStore{fakeStore: newFakeStore(), raw: farm202ClusterRows(500)}
	s := &site{d: Deps{Store: store}}
	r := httptest.NewRequest("GET", "/npm/hot?f_version=2.0.0", nil)
	read := func() {
		rows, total, err := s.loadClusters(r, "npm", "hot", map[string]string{"version": "2.0.0"})
		if err != nil || len(rows) != 0 || total != 0 {
			t.Fatalf("a contradictory pin must keep the same empty result: rows=%d total=%d err=%v", len(rows), total, err)
		}
	}
	read()
	allocs := testing.AllocsPerRun(4, read)
	if allocs > 100 {
		t.Fatalf("repeated page allocated %.0f objects while re-decoding unchanged failure JSON; want <=100", allocs)
	}
}

func TestFarm202ClusterSourceChangesImmediately(t *testing.T) {
	var c decodedClusterCache
	old := farm202ClusterRows(1)
	first, err := c.get(context.Background(), "npm|hot", old)
	if err != nil {
		t.Fatal(err)
	}
	changed := []string{strings.Replace(old[0], "error-0", "error-new", 1)}
	next, err := c.get(context.Background(), "npm|hot", changed)
	if err != nil || next[0].Fingerprint != "error-new" {
		t.Fatalf("changed bytes reused old facts: %v %v", next, err)
	}
	if first[0].Fingerprint != "error-0" {
		t.Fatal("new source mutated an earlier response")
	}
}
func TestFarm202ClusterCacheReleasesEvictedGroups(t *testing.T) {
	c := decodedClusterCache{maxBytes: 5000, maxEntries: 2}
	var calls atomic.Int32
	c.decode = func(raw []string) []failureCluster { calls.Add(1); return decodeFailureClusters(raw) }
	for _, key := range []string{"a", "b", "c"} {
		if _, err := c.get(context.Background(), key, farm202ClusterRows(1)); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.entries) > 2 || c.bytes > 5000 {
		t.Fatalf("retention grew beyond group/byte budget: %d %d", len(c.entries), c.bytes)
	}
	if c.entries["a"] != nil {
		t.Fatal("retired package still owns decoded payload")
	}
	if _, err := c.get(context.Background(), "a", farm202ClusterRows(1)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("evicted package was not freshly parsed: %d", calls.Load())
	}
}
func TestFarm202OversizedClusterGroupIsCompleteWithoutRetention(t *testing.T) {
	c := decodedClusterCache{maxBytes: 1}
	raw := farm202ClusterRows(500)
	for i := 0; i < 2; i++ {
		docs, err := c.get(context.Background(), "big", raw)
		if err != nil || len(docs) != 500 || docs[499].Fingerprint != "error-499" {
			t.Fatalf("oversized input lost facts: rows=%d err=%v", len(docs), err)
		}
	}
	if c.bytes != 0 || len(c.entries) != 0 {
		t.Fatal("oversized group was retained")
	}
}
func TestFarm202ClusterPinsDoNotMutateSharedFacts(t *testing.T) {
	s := &site{}
	raw := farm202ClusterRows(12)
	cached, err := s.decodedClusters(context.Background(), "npm", "hot", raw)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(cached)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			docs, e := s.decodedClusters(context.Background(), "npm", "hot", raw)
			if e != nil {
				t.Error(e)
				return
			}
			version := "1.0.0"
			if i%2 == 0 {
				version = "2.0.0"
			}
			s.loadClustersFrom("npm", "hot", docs, map[string]string{"version": version})
			evaluateCrossReleaseHealth("npm", "hot", docs, nil, "en")
		}(i)
	}
	wg.Wait()
	after, _ := json.Marshal(cached)
	if !bytes.Equal(before, after) {
		t.Fatal("one coordinate changed another request's evidence")
	}
}
func TestFarm202WarmClusterBypassesBusyDecodersAndColdCancellation(t *testing.T) {
	c := decodedClusterCache{}
	var block atomic.Bool
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	c.decode = func(raw []string) []failureCluster {
		if block.Load() {
			entered <- struct{}{}
			<-release
		}
		return decodeFailureClusters(raw)
	}
	raw := farm202ClusterRows(1)
	if _, err := c.get(context.Background(), "warm", raw); err != nil {
		t.Fatal(err)
	}
	block.Store(true)
	for _, key := range []string{"cold-a", "cold-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			if _, err := c.get(context.Background(), key, raw); err != nil {
				t.Error(err)
			}
		}(key)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			wg.Wait()
			t.Fatal("cold decoder did not start")
		}
	}
	done := make(chan error, 1)
	go func() { _, err := c.get(context.Background(), "warm", raw); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		close(release)
		wg.Wait()
		t.Fatal("warm data waited behind unrelated decoding")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.get(ctx, "cancelled", raw); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cold wait did not honor cancellation: %v", err)
	}
	close(release)
	wg.Wait()
	if c.loading["cancelled"] != nil {
		t.Fatal("cancelled cold load leaked coordination state")
	}
}
func TestFarm202MalformedClusterPreservesValidRows(t *testing.T) {
	raw := append([]string{"{broken"}, farm202ClusterRows(2)...)
	c := decodedClusterCache{}
	actual, err := c.get(context.Background(), "mixed", raw)
	if err != nil {
		t.Fatal(err)
	}
	expected := decodeFailureClusters(raw)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("cache changed malformed-row handling")
	}
}
