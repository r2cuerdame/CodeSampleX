package httpapi

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func decodeBody(t *testing.T, body string, out any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), out); err != nil {
		t.Fatalf("bad JSON reply %q: %v", body, err)
	}
}

// storeCallCounter counts the row-at-a-time reads a handler makes against the
// store, so a test can state the footprint of one request as a number that
// must not move with the size of the answer.
//
// It is the same shape as the compatibility builder's read counter: the plain
// counter is the row-at-a-time contract every store satisfies, and
// bulkAPIStore layers the bounded-page contracts PostgreSQL offers on top of
// the identical rows.
type storeCallCounter struct {
	*serverstore.Fake
	mu    sync.Mutex
	calls map[string]int
	pages map[string][]int
	// readErr, when set, is what every counted read answers with. It lets a
	// test play the same failure through both contracts.
	readErr error
}

func newStoreCallCounter(f *serverstore.Fake) *storeCallCounter {
	return &storeCallCounter{Fake: f, calls: map[string]int{}, pages: map[string][]int{}}
}

func (c *storeCallCounter) note(name string, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[name]++
	c.pages[name] = append(c.pages[name], size)
}

func (c *storeCallCounter) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func (c *storeCallCounter) sizes(name string) []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.pages[name]...)
}

func (c *storeCallCounter) GetPackage(ctx context.Context, purl string) (serverstore.PackageRow, bool, error) {
	c.note("GetPackage", 1)
	if c.readErr != nil {
		return serverstore.PackageRow{}, false, c.readErr
	}
	return c.Fake.GetPackage(ctx, purl)
}

func (c *storeCallCounter) GetSnapshot(ctx context.Context, purl, symbol string) (string, bool, error) {
	c.note("GetSnapshot", 1)
	if c.readErr != nil {
		return "", false, c.readErr
	}
	return c.Fake.GetSnapshot(ctx, purl, symbol)
}

func (c *storeCallCounter) ReceiptsForSample(ctx context.Context, sampleID string) ([]serverstore.ReceiptRow, error) {
	c.note("ReceiptsForSample", 1)
	if c.readErr != nil {
		return nil, c.readErr
	}
	return c.Fake.ReceiptsForSample(ctx, sampleID)
}

// rowAtATimeAPIStore is the original contract: one read per row. Alternate
// stores still take this path.
type rowAtATimeAPIStore struct{ *storeCallCounter }

// bulkAPIStore answers the bounded-page contracts the handlers prefer, over
// exactly the same underlying rows.
type bulkAPIStore struct{ *storeCallCounter }

func (s *bulkAPIStore) PackagesByPURL(ctx context.Context, purls []string) (map[string]serverstore.PackageRow, error) {
	s.note("PackagesByPURL", len(purls))
	if s.readErr != nil {
		return nil, s.readErr
	}
	out := make(map[string]serverstore.PackageRow, len(purls))
	for _, purl := range purls {
		if row, ok, err := s.Fake.GetPackage(ctx, purl); err != nil {
			return nil, err
		} else if ok {
			out[purl] = row
		}
	}
	return out, nil
}

func (s *bulkAPIStore) SnapshotsForPURLs(ctx context.Context, purls []string, symbol string) (map[string]string, error) {
	s.note("SnapshotsForPURLs", len(purls))
	if s.readErr != nil {
		return nil, s.readErr
	}
	out := make(map[string]string, len(purls))
	for _, purl := range purls {
		if js, ok, err := s.Fake.GetSnapshot(ctx, purl, symbol); err != nil {
			return nil, err
		} else if ok {
			out[purl] = js
		}
	}
	return out, nil
}

func (s *bulkAPIStore) ReceiptsForSamples(ctx context.Context, sampleIDs []string) (map[string][]serverstore.ReceiptRow, error) {
	s.note("ReceiptsForSamples", len(sampleIDs))
	if s.readErr != nil {
		return nil, s.readErr
	}
	out := make(map[string][]serverstore.ReceiptRow, len(sampleIDs))
	for _, sampleID := range sampleIDs {
		rows, err := s.Fake.ReceiptsForSample(ctx, sampleID)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			out[sampleID] = rows
		}
	}
	return out, nil
}

func equalIntSlices(a, b []int) bool {
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
