package evidence

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Production's transaction committed a 245-row request, but its response
// arrived after the server's write deadline. A persisted row alone is not an ACK.
func TestUploadDrainsSlowBacklogWithinEachServerWriteDeadline(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	env := testEnvFP()
	if err := db.SaveEnvironment(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	const backlog = 245
	for i := 0; i < backlog; i++ {
		if err := db.RecordObservation(t.Context(), localdb.ObsKey{
			Epoch: "2026-10-09", PURL: fmt.Sprintf("pkg:npm/slow-ack-%04d@1.0.0", i),
			EnvHash: env.Hash(), Stage: domain.StageUsed, Result: domain.ResultPass,
		}, 1); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batches []json.RawMessage `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		time.Sleep(time.Duration(len(body.Batches)) * 2 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
	}))
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	defer srv.Close()
	b := &Batcher{DB: db, Ident: ident, Cfg: communityCfg(srv.URL)}
	sent, err := b.Upload(t.Context(), srv.Client(), srv.URL)
	if err != nil || sent != backlog {
		t.Fatalf("acknowledged %d/%d: %v", sent, backlog, err)
	}
	if rows := pendingRows(t, db); len(rows) != 0 {
		t.Fatalf("%d rows still pending after ACKs", len(rows))
	}
}
