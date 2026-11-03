package main

import (
	"container/list"
	"sync"
)

const (
	sampleArtifactCacheBytes   = 32 << 20
	sampleArtifactCacheEntries = 512
)

type sampleArtifactCacheEntry struct {
	id    string
	value cachedDecodedArtifact
	bytes int
}

type sampleArtifactCache struct {
	mu      sync.Mutex
	order   list.List
	entries map[string]*list.Element
	bytes   int
}

func sampleArtifactSize(id string, a decodedArtifact) int {
	size := 256 + len(id) + 16*cap(a.files) + 48*cap(a.source)
	for _, f := range a.files {
		size += len(f)
	}
	for _, f := range a.source {
		size += len(f.Name) + len(f.Body)
	}
	return size
}

func (c *sampleArtifactCache) Load(id string) (cachedDecodedArtifact, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.entries[id]
	if element == nil {
		return cachedDecodedArtifact{}, false
	}
	c.order.MoveToBack(element)
	return element.Value.(*sampleArtifactCacheEntry).value, true
}

func (c *sampleArtifactCache) Store(id string, value cachedDecodedArtifact) {
	size := sampleArtifactSize(id, value.artifact)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	c.remove(c.entries[id])
	if size > sampleArtifactCacheBytes {
		return
	}
	for c.bytes+size > sampleArtifactCacheBytes || len(c.entries) >= sampleArtifactCacheEntries {
		c.remove(c.order.Front())
	}
	c.entries[id] = c.order.PushBack(&sampleArtifactCacheEntry{id: id, value: value, bytes: size})
	c.bytes += size
}

func (c *sampleArtifactCache) usage() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.bytes
}

func (c *sampleArtifactCache) remove(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*sampleArtifactCacheEntry)
	delete(c.entries, entry.id)
	c.order.Remove(element)
	c.bytes -= entry.bytes
}
