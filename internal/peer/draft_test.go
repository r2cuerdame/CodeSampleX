package peer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// `csx sample create` on a draft cut from a private repository produced a
// locally cached artifact, and the announce loop offered its id to the
// tracker within ten minutes. Anyone who learned the id — starting with
// the tracker itself — could then fetch the draft's source from the peer
// port. The network has no business distributing what its author has not
// published.
func TestADraftIsNeverAnnouncedOrServed(t *testing.T) {
	dir := t.TempDir()
	db, err := localdb.Open(filepath.Join(dir, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := cas.Open(filepath.Join(dir, "cas"))
	if err != nil {
		t.Fatal(err)
	}
	ident, err := identity.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	draft := "sha256:" + strings.Repeat("d1", 32)
	live := "sha256:" + strings.Repeat("e2", 32)
	for _, s := range []struct{ id, status string }{{draft, "LOCAL_PASS"}, {live, "PUBLISHED"}} {
		if err := db.SaveSample(ctx, localdb.SampleRow{
			SampleID: s.id, Status: s.status, HasArtifact: true,
			ManifestJSON: `{"schemaVersion":1}`, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var got struct {
		SampleIDs []string `json:"sampleIds"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &Node{CAS: store, DB: db, Ident: ident, ServerURL: srv.URL, Port: 48620}
	if err := n.Announce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range got.SampleIDs {
		if id == draft {
			t.Error("an unpublished draft was announced to the tracker")
		}
	}
	var sawLive bool
	for _, id := range got.SampleIDs {
		if id == live {
			sawLive = true
		}
	}
	if !sawLive {
		t.Error("a published sample stopped being announced")
	}

	// Even with the exact id, the draft is not served.
	rec := httptest.NewRecorder()
	n.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/peer/v1/samples/"+draft, nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("the peer served a draft artifact: status %d", rec.Code)
	}
}

var _ = context.Background
