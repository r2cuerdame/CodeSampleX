package web

import (
	"bytes"
	"container/list"
	"encoding/json"
	"html/template"
	"net/url"
	"sync"
)

// Only bytes of an error already selected by the current request are reused.
// This never caches a route outcome or skips a store read. Every request keeps
// its status, counters, Retry-After and no-store headers.
const errorRenderBudget = 1 << 20
const errorRenderEntries = 32

type renderedErrorEntry struct {
	key   string
	body  string
	bytes int
}
type errorRenderCache struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      list.List
	bytes      int
	maxBytes   int
	maxEntries int
}

func (c *errorRenderCache) render(t *template.Template, data errorPage) (string, bool) {
	// Include all exported template data, and the unexported URL state used by
	// LangLinks. A different locale, host, query, build or related link cannot
	// inherit another request's rendered chrome.
	query := data.query
	if query == nil {
		query = url.Values{}
	}
	raw, err := json.Marshal(struct {
		Page      errorPage
		Path      string
		Query     string
		CanonLang string
	}{data, data.path, query.Encode(), data.canonLang})
	if err != nil || len(raw) > 8192 {
		return "", false
	}
	key := string(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el := c.entries[key]; el != nil {
		c.order.MoveToFront(el)
		return el.Value.(*renderedErrorEntry).body, true
	}
	// Serialise only cold error rendering, so an error burst cannot create one
	// template execution per waiter. Successful HTML and API/Farm routes do not
	// use this lock or cache.
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "base.html", data); err != nil {
		return "", false
	}
	body := b.String()
	size := 128 + len(key) + len(body)
	maxBytes, maxEntries := c.maxBytes, c.maxEntries
	if maxBytes <= 0 {
		maxBytes = errorRenderBudget
	}
	if maxEntries <= 0 {
		maxEntries = errorRenderEntries
	}
	if size > maxBytes {
		return body, true
	}
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	for c.bytes+size > maxBytes || len(c.entries) >= maxEntries {
		el := c.order.Back()
		e := el.Value.(*renderedErrorEntry)
		delete(c.entries, e.key)
		c.bytes -= e.bytes
		c.order.Remove(el)
	}
	e := &renderedErrorEntry{key: key, body: body, bytes: size}
	c.entries[key] = c.order.PushFront(e)
	c.bytes += size
	return body, true
}
