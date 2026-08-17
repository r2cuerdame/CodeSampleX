package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// queueReadLimit lets a wanted batch reach rows that are interleaved behind
// adoption feedback in the FIFO.  queueDrainLimit below still caps the
// remaining one-request-per-row deliveries.
const queueReadLimit = 200

// wantedBatchLimit mirrors the public API envelope. One batch per drain
// pass keeps old clients from turning a backlog into an amplification burst;
// remaining wanted rows stay durable for the next daemon tick.
const wantedBatchLimit = 20

// wantedPublicCheckTimeout bounds the whole pre-upload registry pass, not
// each package. Search already returned before this work began; a slow or
// unavailable registry delays the background queue only for this long.
const wantedPublicCheckTimeout = 5 * time.Second

// queueDrainLimit bounds one drain pass below the server's shared write
// budget.  Sending 200 one-row reports to a 60/minute endpoint guaranteed a
// 429 halfway through every sync, then counted those throttles as delivery
// attempts until useful reports were set aside.  Forty leaves room for
// evidence, receipts and other peers behind the same address.
const queueDrainLimit = 40

// queueEndpoints maps a queued item's kind to the route that accepts it.
// A kind with no route is left alone rather than dropped: the payload is
// the user's contribution and a future build may know where it goes.
var queueEndpoints = map[string]string{
	"adoption":                        "/v1/adoptions",
	"wanted":                          "/v1/wanted",
	evidence.WantedCandidateQueueKind: "/v1/wanted",
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
	items, err := d.DB.QueuePending(ctx, queueReadLimit)
	if err != nil {
		return 0, err
	}
	base := strings.TrimRight(d.Cfg.ServerURL, "/")
	sent := 0
	var firstErr error

	// Wanted candidates are intentionally queued before registry I/O so an
	// MCP NO_SAFE_MATCH response never waits on a third party. Recheck every
	// candidate here, immediately before transmission, and marshal the fixed
	// wire type again. The server performs the same fail-closed check.
	//
	// Reports share one wire shape and server-side dedup makes the whole
	// group safe to retry. Sending a bounded group as one request prevents a
	// real backlog from consuming one request-budget token per report.
	processed := map[int64]bool{}
	preparedPayloads := map[int64]string{}
	var wantedItems []localdb.QueueItem
	var wantedReports []json.RawMessage
	checkCtx, cancelChecks := context.WithTimeout(ctx, wantedPublicCheckTimeout)
	defer cancelChecks()
	publicness, checked := map[string]bool{}, map[string]bool{}
	isPublic := func(checkCtx context.Context, p domain.PURL) bool {
		key := p.String()
		if checked[key] {
			return publicness[key]
		}
		checked[key] = true
		if d.Cfg.IsExcluded(key, p.Ecosystem, p.Name) || d.WantedPublic == nil {
			return false
		}
		publicness[key] = d.WantedPublic(checkCtx, p)
		return publicness[key]
	}
	wantedCandidatesChecked := 0
	for _, it := range items {
		if it.Kind != "wanted" && it.Kind != evidence.WantedCandidateQueueKind {
			continue
		}
		if wantedCandidatesChecked >= wantedBatchLimit {
			processed[it.ID] = true // deferred, not acknowledged
			continue
		}
		wantedCandidatesChecked++
		clean, prepErr := evidence.PrepareWantedForUpload(checkCtx, it.Payload, isPublic)
		if prepErr != nil {
			if ctx.Err() != nil {
				return sent, ctx.Err()
			}
			if errors.Is(prepErr, evidence.ErrWantedPublicnessUnconfirmed) {
				// UNKNOWN and private deliberately share the same false result.
				// Retry a bounded number of later drains; never guess public and
				// never let an unconfirmed row fall through to the POST path.
				_ = d.DB.QueueMarkFailed(ctx, it.ID, prepErr.Error())
			} else {
				_ = d.DB.QueueSetAside(ctx, it.ID, prepErr.Error())
			}
			processed[it.ID] = true
			continue
		}
		wantedItems = append(wantedItems, it)
		preparedPayloads[it.ID] = string(clean)
		wantedReports = append(wantedReports, json.RawMessage(clean))
	}
	if len(wantedReports) > 0 {
		payload, _ := json.Marshal(map[string]any{
			"schemaVersion": 1,
			"reports":       wantedReports,
		})
		err := d.postQueued(ctx, base+"/v1/wanted/batches", string(payload))
		var throttle *queueThrottle
		switch {
		case err == nil:
			for _, it := range wantedItems {
				if markErr := d.DB.QueueMarkDone(ctx, it.ID); markErr != nil {
					return sent, markErr
				}
				processed[it.ID] = true
				sent++
			}
		case errors.As(err, &throttle):
			return sent, err
		case permanentDeliveryFailure(err):
			// A server predating the batch route, or one malformed legacy
			// row, falls back to the existing per-row path below.  This is
			// rollout-compatible and isolates a bad row instead of losing all.
			firstErr = err
		default:
			// A network or server failure applies to the delivery channel, not
			// to 200 individual reports. Retrying them immediately would both
			// hammer the same unhealthy server and consume their local attempt
			// budgets for one outage.
			return sent, err
		}
	}

	individual := 0
	for _, it := range items {
		if processed[it.ID] {
			continue
		}
		if individual >= queueDrainLimit {
			break
		}
		path, ok := queueEndpoints[it.Kind]
		if !ok {
			continue
		}
		individual++
		payload := it.Payload
		if clean, ok := preparedPayloads[it.ID]; ok {
			payload = clean
		}
		if err := d.postQueued(ctx, base+path, payload); err != nil {
			var throttle *queueThrottle
			if errors.As(err, &throttle) {
				// A throttle says nothing about this report.  Do not consume a
				// retry and stop the pass so later rows are not predictably sent
				// into the same closed window.  The daemon's next queue tick is
				// later than the server's Retry-After.
				firstErr = err
				break
			}
			// A rejection the server will repeat forever is set aside now
			// rather than retried 8 more times: it is already at the head
			// of a FIFO with a drain limit, and every pass it survives is
			// a slot a deliverable report does not get.
			if permanentDeliveryFailure(err) {
				_ = d.DB.QueueSetAside(ctx, it.ID, err.Error())
			} else {
				_ = d.DB.QueueMarkFailed(ctx, it.ID, err.Error())
			}
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

// permanentFailure marks a rejection that retrying cannot fix.
type permanentFailure struct{ error }

type queueThrottle struct {
	after time.Duration
}

func (e *queueThrottle) Error() string {
	if e.after > 0 {
		return fmt.Sprintf("HTTP 429 (retry after %s)", e.after)
	}
	return "HTTP 429"
}

func permanentDeliveryFailure(err error) bool {
	var p permanentFailure
	return errors.As(err, &p)
}

func (d *Daemon) postQueued(ctx context.Context, url, payload string) error {
	if !json.Valid([]byte(payload)) {
		return permanentFailure{fmt.Errorf("queued payload is not valid JSON")}
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
	// 4xx means this payload will never be accepted, so it is set aside;
	// 5xx and 429 are worth another pass.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return &queueThrottle{after: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
		}
		err := fmt.Errorf("HTTP %d", resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return permanentFailure{err}
		}
		return err
	}
	return nil
}

func retryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(raw + "s"); err == nil && seconds > 0 {
		return seconds
	}
	if when, err := http.ParseTime(raw); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}
