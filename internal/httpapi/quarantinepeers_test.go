package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Quarantine says it hides the sample from EVERY serving read, and the
// operator is told exactly that. The sample and artifact endpoints honoured
// it; the peer list did not — so a takedown still handed out a list of
// peers holding the bytes, and the fetch chain tries peers BEFORE the
// seeder, which is precisely how a client would still get the sample the
// operator had just removed.
func TestQuarantineHidesThePeerListToo(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	ctx := t.Context()
	id := "sha256:" + strings.Repeat("d1", 32)
	saveSearchable(t, store, id, testManifest())

	_, peerID := newPeer(t)
	body, _ := json.Marshal(map[string]any{
		"peerId": peerID, "port": 48620,
		"capabilities": []string{"CONTAINER_RUN"},
		"sampleIds":    []string{id},
		"ttlSeconds":   600,
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/peers/announce", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var before struct {
		Peers []struct {
			PeerID string `json:"peerId"`
		} `json:"peers"`
	}
	getJSON(t, srv.URL+"/v1/peers/for-sample/"+id, &before)
	if len(before.Peers) != 1 {
		t.Fatalf("setup: peers = %d, want 1", len(before.Peers))
	}

	if err := store.SetSampleQuarantine(ctx, id, true, "malware"); err != nil {
		t.Fatal(err)
	}
	var after struct {
		Peers []struct {
			PeerID string `json:"peerId"`
		} `json:"peers"`
	}
	getJSON(t, srv.URL+"/v1/peers/for-sample/"+id, &after)
	if len(after.Peers) != 0 {
		t.Errorf("a quarantined sample still advertises %d peer(s) holding it", len(after.Peers))
	}
}

var _ = serverstore.SampleRow{}
var _ = domain.SampleManifest{}
