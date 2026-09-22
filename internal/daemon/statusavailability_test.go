package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

// GET /local/v1/status is what the Farm samples for queue depth. When the
// queue could not be read it used to answer 200 with depth 0 — the same
// bytes as a healthy, empty queue — so a broken read looked like recovery
// (#377). A read that failed must say so on the wire.
func TestStatusNamesAnUnreadableQueueInsteadOfReportingZero(t *testing.T) {
	for _, index := range []string{"observations_pending", "upload_queue_pending"} {
		t.Run(index, func(t *testing.T) {
			home := newTestHome(t, nil)
			d, c := startDaemon(t, home)
			ctx := context.Background()
			if _, err := d.DB.Enqueue(ctx, "adoption", `{"schemaVersion":1}`); err != nil {
				t.Fatal(err)
			}
			healthy, err := c.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if healthy.QueueUnavailable || healthy.QueueError != "" || healthy.QueueDepth != 1 {
				t.Fatalf("healthy status misreported: %+v", healthy)
			}

			raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(home, "csx.db")))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.ExecContext(ctx, "DROP INDEX "+index); err != nil {
				t.Fatal(err)
			}

			res, err := http.Get(d.BaseURL() + "/local/v1/status")
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status must stay reachable while the queue is unreadable: %d", res.StatusCode)
			}
			var body map[string]any
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["queueUnavailable"] != true {
				t.Fatalf("an unreadable queue was reported as measured: %v", body)
			}
			if reason, _ := body["queueError"].(string); reason == "" {
				t.Fatalf("no reason for the unavailable queue: %v", body)
			}
			// The typed client sees the same thing, and the depth it carries
			// is not a claim: a consumer must check the flag before the number.
			st, err := c.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !st.QueueUnavailable {
				t.Fatalf("client status lost the availability flag: %+v", st)
			}
		})
	}
}
