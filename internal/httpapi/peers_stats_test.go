package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPeerAnnounceAndForSample(t *testing.T) {
	srv, _, ck := newTestServer(t, nil)
	_, peerID := newPeer(t)
	sampleID := "sha256:" + strings.Repeat("aa", 32)

	// TTL below the floor is clamped to 60; X-Forwarded-For wins as addr.
	body, _ := json.Marshal(map[string]any{
		"peerId": peerID, "port": 48620,
		"capabilities": []string{"CONTAINER_RUN"},
		"sampleIds":    []string{sampleID},
		"ttlSeconds":   10,
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/peers/announce", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("announce status = %d", resp.StatusCode)
	}
	var ann struct {
		Addr       string `json:"addr"`
		TTLSeconds int    `json:"ttlSeconds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ann); err != nil {
		t.Fatal(err)
	}
	if ann.Addr != "203.0.113.7" {
		t.Fatalf("addr = %q, want X-Forwarded-For first hop", ann.Addr)
	}
	if ann.TTLSeconds != 60 {
		t.Fatalf("ttl = %d, want clamped 60", ann.TTLSeconds)
	}

	var peers struct {
		Peers []struct {
			PeerID string `json:"peerId"`
			Addr   string `json:"addr"`
			Port   int    `json:"port"`
		} `json:"peers"`
	}
	getJSON(t, srv.URL+"/v1/peers/for-sample/"+sampleID, &peers)
	if len(peers.Peers) != 1 || peers.Peers[0].PeerID != peerID || peers.Peers[0].Port != 48620 {
		t.Fatalf("peers = %+v", peers.Peers)
	}

	// After the TTL passes the peer expires out of the tracker response.
	ck.t = ck.t.Add(2 * time.Minute)
	getJSON(t, srv.URL+"/v1/peers/for-sample/"+sampleID, &peers)
	if len(peers.Peers) != 0 {
		t.Fatalf("expired peer still served: %+v", peers.Peers)
	}
}

func TestPeerAnnounceValidation(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp := postJSON(t, srv.URL+"/v1/peers/announce",
		map[string]any{"peerId": "bogus", "port": 48620}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad peerId status = %d, want 400", resp.StatusCode)
	}
	_, peerID := newPeer(t)
	resp = postJSON(t, srv.URL+"/v1/peers/announce",
		map[string]any{"peerId": peerID, "port": 0}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad port status = %d, want 400", resp.StatusCode)
	}
	// TTL above the ceiling is clamped to 7200.
	var ann struct {
		TTLSeconds int `json:"ttlSeconds"`
	}
	resp = postJSON(t, srv.URL+"/v1/peers/announce",
		map[string]any{"peerId": peerID, "port": 48620, "ttlSeconds": 999999}, &ann)
	if resp.StatusCode != http.StatusOK || ann.TTLSeconds != 7200 {
		t.Fatalf("ttl clamp: status=%d ttl=%d", resp.StatusCode, ann.TTLSeconds)
	}
}

// --- stats ---------------------------------------------------------------------

func TestStatsCarriesEstimatedFlagAlways(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)

	// Before any builder pass: computed live, still labeled.
	var stats map[string]any
	resp := getJSON(t, srv.URL+"/v1/stats", &stats)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	est, ok := stats["estimatedReasoningAvoided"].(map[string]any)
	if !ok || est["estimated"] != true {
		t.Fatalf("estimatedReasoningAvoided = %v, want estimated:true", stats["estimatedReasoningAvoided"])
	}
	if _, ok := stats["postHitBuildPass"]; !ok {
		t.Fatal("postHitBuildPass placeholder missing")
	}

	// A stored daily rollup is served verbatim.
	stored := `{"schemaVersion":1,"day":"2026-08-13","peers":7,` +
		`"estimatedReasoningAvoided":{"estimated":true,"value":0,"formula":"hitsAdopted * 3","assumptions":[]}}`
	if err := store.SetStatsDaily(t.Context(), "2026-08-13", stored); err != nil {
		t.Fatal(err)
	}
	resp = getJSON(t, srv.URL+"/v1/stats", &stats)
	if resp.StatusCode != http.StatusOK || stats["peers"] != float64(7) {
		t.Fatalf("stored stats not served: %v", stats)
	}
}

// --- adapters --------------------------------------------------------------------

// TestAdaptersMatchesSchemaFile pins the embedded copy to the canonical
// schemas/v1/adapters.json byte for byte, so the two can never drift.
func TestAdaptersMatchesSchemaFile(t *testing.T) {
	canonical, err := os.ReadFile("../../schemas/v1/adapters.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(adaptersJSON) != string(canonical) {
		t.Fatal("internal/httpapi/adapters.json differs from schemas/v1/adapters.json — copy it verbatim")
	}

	srv, _, _ := newTestServer(t, nil)
	resp, err := http.Get(srv.URL + "/v1/adapters")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if string(body) != string(canonical) {
		t.Fatal("GET /v1/adapters body differs from schemas/v1/adapters.json")
	}
	var doc struct {
		Adapters []struct {
			Capabilities []string `json:"capabilities"`
		} `json:"adapters"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || len(doc.Adapters) != 4 {
		t.Fatalf("adapters doc: %v, adapters=%d", err, len(doc.Adapters))
	}
	for _, a := range doc.Adapters {
		for _, c := range a.Capabilities {
			if c == "A3" {
				t.Fatal("no Public v1 adapter may claim A3")
			}
		}
	}
}

// --- healthz ----------------------------------------------------------------------

func TestHealthz(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}
}
