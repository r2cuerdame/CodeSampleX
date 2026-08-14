package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// queueDrainLimit bounds one drain pass.
const queueDrainLimit = 200

// queueEndpoints maps a queued item's kind to the route that accepts it.
// A kind with no route is left alone rather than dropped: the payload is
// the user's contribution and a future build may know where it goes.
var queueEndpoints = map[string]string{
	"adoption": "/v1/adoptions",
}

// drainQueue uploads whatever is sitting in the local upload queue.
//
// Nothing ever did. `report_sample_adoption` enqueued a row, `csx sync`
// reported "uploaded batches: 0" and exited 0, and the row stayed there
// forever — the server had no route to receive one either. So the far end
// of the loop this product describes, ask → verified answer → report
// whether it worked, was never connected, and the site's post-hit success
// rate was a hardcoded zero with a comment explaining that adoption
// reporting had not reached the server yet.
//
// It is the only feedback the network gets about whether its answers are
// any good. Every other number describes how much the network KNOWS.
//
// Community mode only, like every upload. A failed item is counted and
// left in place: the queue is the retry, and dropping a report because a
// server was briefly down would lose the one signal that cannot be
// recomputed from anything else.
func (d *Daemon) drainQueue(ctx context.Context) (int, error) {
	if d.Cfg.Mode != "community" {
		return 0, nil
	}
	items, err := d.DB.QueuePending(ctx, queueDrainLimit)
	if err != nil {
		return 0, err
	}
	base := strings.TrimRight(d.Cfg.ServerURL, "/")
	sent := 0
	var firstErr error
	for _, it := range items {
		path, ok := queueEndpoints[it.Kind]
		if !ok {
			continue
		}
		if err := d.postQueued(ctx, base+path, it.Payload); err != nil {
			_ = d.DB.QueueMarkFailed(ctx, it.ID, err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := d.DB.QueueMarkDone(ctx, it.ID); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, firstErr
}

func (d *Daemon) postQueued(ctx context.Context, url, payload string) error {
	if !json.Valid([]byte(payload)) {
		// Unparseable payloads can never be delivered; leaving them queued
		// would retry forever, so this is the one case that is dropped —
		// loudly, in the returned error.
		return fmt.Errorf("queued payload is not valid JSON")
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, url, bytes.NewReader([]byte(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 4xx means this payload will never be accepted, so the caller marks it
	// failed and it stops being retried by attempt count; 5xx is worth
	// another pass.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
