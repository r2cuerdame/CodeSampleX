package daemon

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The MCP path stopped queueing adoption uploads outside community mode
// hours before this one did: the same report, arriving through the local
// HTTP API instead of the tool, was still written into a queue named
// "upload" on a machine whose whole promise is that nothing is uploaded.
//
// Nothing drained it there, so nothing actually left. The row was written
// on the promise that it would be sent, which is the part that was false.
func TestAdoptionIsNotQueuedOutsideCommunityMode(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{config.ModeLocalOnly, config.ModeUninitialized} {
		home := newTestHome(t, func(cfg *config.Config) { cfg.Mode = mode })
		d, c := startDaemon(t, home)
		seedSample(t, d, "sha256:ccc3")
		resp, err := c.Search(ctx, domain.SearchRequest{
			SchemaVersion: 1,
			Query:         "upload multipart form with axios",
			Packages:      []string{"pkg:npm/axios@1.12.0"},
			Environment:   testEnv(),
		})
		if err != nil || resp.Miss || len(resp.Results) == 0 || resp.OfferID == "" {
			t.Fatalf("mode %q: search before adoption: resp=%+v err=%v", mode, resp, err)
		}

		pass := true
		if err := c.Adopt(ctx, AdoptionRequest{OfferID: resp.OfferID, SampleID: "sha256:ccc3", Applied: true, BuildPass: &pass}); err != nil {
			t.Fatalf("mode %q: adopt: %v", mode, err)
		}
		// The local record is kept — that is what list_local_hits reads.
		hits, err := d.DB.ListHits(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 {
			t.Errorf("mode %q: the adoption was not recorded locally either", mode)
		}
		items, err := d.DB.QueuePending(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Errorf("mode %q: queued %d upload(s) on an install that uploads nothing", mode, len(items))
		}
	}
}
