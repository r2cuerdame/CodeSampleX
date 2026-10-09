package web

import (
	"container/list"
	"context"
	"reflect"
	"sync"
)

// A package crawl repeatedly decoded the same display documents even though
// the store already shared the JSON. Keep immutable parsed facts, with an
// accounted 16 MiB payload budget and 64-package LRU. Accounting includes raw
// strings and decoded dynamic values; it is not a Go allocator/RSS limit.
const clusterDecodeBudget = 16 << 20
const clusterDecodePackages = 64

type decodedClusterEntry struct {
	key   string
	raw   []string
	docs  []failureCluster
	bytes int64
}
type clusterDecodeCall struct {
	raw  []string
	done chan struct{}
	docs []failureCluster
	err  error
}
type decodedClusterCache struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      list.List
	loading    map[string]*clusterDecodeCall
	slots      chan struct{}
	bytes      int64
	maxBytes   int64
	maxEntries int
	decode     func([]string) []failureCluster // deterministic test seam
}

func sameClusterDocuments(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *site) decodedClusters(ctx context.Context, eco, name string, raw []string) ([]failureCluster, error) {
	return s.clusterCache.get(ctx, eco+"|"+name, raw)
}

// Returned documents are read-only. Pin filtering and view construction build
// separate output slices; a request must never alter these shared facts.
func (c *decodedClusterCache) get(ctx context.Context, key string, raw []string) ([]failureCluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return decodeFailureClusters(raw), nil
	}
	for {
		c.mu.Lock()
		if c.entries == nil {
			c.entries = make(map[string]*list.Element)
			c.loading = make(map[string]*clusterDecodeCall)
			c.slots = make(chan struct{}, 2)
		}
		if el := c.entries[key]; el != nil {
			entry := el.Value.(*decodedClusterEntry)
			if sameClusterDocuments(entry.raw, raw) {
				c.order.MoveToFront(el)
				docs := entry.docs
				c.mu.Unlock()
				return docs, nil
			}
		}
		if call := c.loading[key]; call != nil {
			matches := sameClusterDocuments(call.raw, raw)
			c.mu.Unlock()
			select {
			case <-call.done:
				if matches {
					return call.docs, call.err
				}
				// Changed source bytes must not inherit an older in-flight result.
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		call := &clusterDecodeCall{raw: append([]string(nil), raw...), done: make(chan struct{})}
		c.loading[key] = call
		slots, decode := c.slots, c.decode
		c.mu.Unlock()

		// Only cold decoding uses this gate. Cached packages and all API/Farm
		// routes bypass it; waiting callers can leave on request cancellation.
		select {
		case slots <- struct{}{}:
			if decode == nil {
				decode = decodeFailureClusters
			}
			call.docs = decode(call.raw)
			<-slots
		case <-ctx.Done():
			call.err = ctx.Err()
		}
		size := int64(256+len(key)) + retainedClusterDynamic(reflect.ValueOf(call.raw)) + retainedClusterDynamic(reflect.ValueOf(call.docs))
		c.mu.Lock()
		if call.err == nil {
			if el := c.entries[key]; el != nil {
				c.remove(el)
			}
			maxBytes, maxEntries := c.maxBytes, c.maxEntries
			if maxBytes <= 0 {
				maxBytes = clusterDecodeBudget
			}
			if maxEntries <= 0 {
				maxEntries = clusterDecodePackages
			}
			if size <= maxBytes {
				for c.bytes+size > maxBytes || len(c.entries) >= maxEntries {
					c.remove(c.order.Back())
				}
				entry := &decodedClusterEntry{key: key, raw: call.raw, docs: call.docs, bytes: size}
				c.entries[key] = c.order.PushFront(entry)
				c.bytes += size
			}
		}
		delete(c.loading, key)
		close(call.done)
		c.mu.Unlock()
		// Oversized groups still reach every waiter in full, without retention.
		return call.docs, call.err
	}
}

func (c *decodedClusterCache) remove(el *list.Element) {
	if el == nil {
		return
	}
	entry := el.Value.(*decodedClusterEntry)
	delete(c.entries, entry.key)
	c.bytes -= entry.bytes
	c.order.Remove(el)
}

// Count backing arrays, string data, pointers, and a conservative per-map
// allowance. Fixed struct fields already belong to their containing array;
// their dynamic children are counted here once.
func retainedClusterDynamic(v reflect.Value) int64 {
	if !v.IsValid() {
		return 0
	}
	switch v.Kind() {
	case reflect.String:
		return int64(v.Len())
	case reflect.Slice:
		n := int64(v.Cap()) * int64(v.Type().Elem().Size())
		for i := 0; i < v.Len(); i++ {
			n += retainedClusterDynamic(v.Index(i))
		}
		return n
	case reflect.Map:
		if v.IsNil() {
			return 0
		}
		n := int64(64) + int64(v.Len())*(int64(v.Type().Key().Size()+v.Type().Elem().Size())+64)
		it := v.MapRange()
		for it.Next() {
			n += retainedClusterDynamic(it.Key()) + retainedClusterDynamic(it.Value())
		}
		return n
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return 0
		}
		return int64(v.Elem().Type().Size()) + retainedClusterDynamic(v.Elem())
	case reflect.Struct, reflect.Array:
		var n int64
		if v.Kind() == reflect.Struct {
			for i := 0; i < v.NumField(); i++ {
				n += retainedClusterDynamic(v.Field(i))
			}
		} else {
			for i := 0; i < v.Len(); i++ {
				n += retainedClusterDynamic(v.Index(i))
			}
		}
		return n
	}
	return 0
}
