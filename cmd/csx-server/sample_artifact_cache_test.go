package main

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

func randomSource(n int, seed int64) string {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a' + byte(r.Intn(26))
	}
	return string(b)
}

const samplesMaxView = samples.MaxViewBytes

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func TestSampleArtifactCacheRetentionIsBounded(t *testing.T) {
	const samples = 800
	const bodyBytes = samplesMaxView
	blobs := &fakeBlobStore{blobs: map[string][]byte{}}
	for i := 0; i < samples; i++ {
		blobs.blobs[fmt.Sprintf("sample-%d", i)] = testTgzOf(t, [][2]string{{"a.go", randomSource(bodyBytes, int64(i))}, {"b.go", randomSource(bodyBytes, int64(-i-1))}})
	}
	w := &webStore{s: serverstore.NewFake(), blobs: blobs}
	ctx := context.Background()
	before := liveHeap()
	for i := 0; i < samples; i++ {
		got, err := w.loadSampleArtifact(ctx, fmt.Sprintf("sample-%d", i))
		if err != nil || len(got.source) != 2 || len(got.source[0].Body) != bodyBytes {
			t.Fatalf("sample %d: err=%v source=%d", i, err, len(got.source))
		}
	}
	after := liveHeap()
	runtime.KeepAlive(w)
	growth := int64(after) - int64(before)
	t.Logf("retained heap after %d distinct 2x%d KiB samples: %.1f MiB", samples, bodyBytes>>10, float64(growth)/(1<<20))
	if growth > 48<<20 {
		t.Fatalf("decoded sample sources retained %.1f MiB, want at most 48 MiB", float64(growth)/(1<<20))
	}
}

func TestSampleArtifactCacheEvictsLeastRecentAndReloads(t *testing.T) {
	blobs := &fakeBlobStore{blobs: map[string][]byte{}}
	for i := 0; i <= sampleArtifactCacheEntries; i++ {
		blobs.blobs[fmt.Sprintf("sample-%d", i)] = testTgzOf(t, [][2]string{{"a.txt", fmt.Sprintf("body %d", i)}})
	}
	w := &webStore{s: serverstore.NewFake(), blobs: blobs}
	ctx := context.Background()
	for i := 0; i < sampleArtifactCacheEntries; i++ {
		if _, err := w.loadSampleArtifact(ctx, fmt.Sprintf("sample-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.loadSampleArtifact(ctx, "sample-0"); err != nil {
		t.Fatal(err)
	}
	gets := blobs.gets.Load()
	if _, err := w.loadSampleArtifact(ctx, fmt.Sprintf("sample-%d", sampleArtifactCacheEntries)); err != nil {
		t.Fatal(err)
	}
	if entries, _ := w.sampleArtifacts.usage(); entries != sampleArtifactCacheEntries {
		t.Fatalf("entries=%d, want %d", entries, sampleArtifactCacheEntries)
	}
	if _, err := w.loadSampleArtifact(ctx, "sample-0"); err != nil {
		t.Fatal(err)
	}
	if got := blobs.gets.Load() - gets; got != 1 {
		t.Fatalf("recently used sample was evicted: blob reads=%d, want 1", got)
	}
	got, err := w.loadSampleArtifact(ctx, "sample-1")
	if err != nil || len(got.source) != 1 || got.source[0].Body != "body 1" {
		t.Fatalf("evicted sample reload: err=%v source=%v", err, got.source)
	}
	if reads := blobs.gets.Load() - gets; reads != 2 {
		t.Fatalf("evicted sample blob reads=%d, want 2", reads)
	}
}

func TestSampleArtifactCacheByteBound(t *testing.T) {
	var c sampleArtifactCache
	body := randomSource(1<<20, 1)
	for i := 0; i < 64; i++ {
		c.Store(fmt.Sprintf("big-%d", i), cachedDecodedArtifact{artifact: decodedArtifact{source: []web.SampleFile{{Name: "f", Body: body}}}})
	}
	entries, bytes := c.usage()
	if bytes > sampleArtifactCacheBytes || entries >= 64 {
		t.Fatalf("entries=%d bytes=%d, budget %d", entries, bytes, sampleArtifactCacheBytes)
	}
	c.Store("huge", cachedDecodedArtifact{artifact: decodedArtifact{source: []web.SampleFile{{Name: "f", Body: randomSource(sampleArtifactCacheBytes+1, 2)}}}})
	if _, ok := c.Load("huge"); ok {
		t.Fatal("an artifact larger than the whole budget must not be retained")
	}
}
