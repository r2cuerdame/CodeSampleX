package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Peer announce TTL bounds (seconds).
const (
	peerTTLMin     = 60
	peerTTLMax     = 7200
	peerTTLDefault = 1800
)

// handlePeerAnnounce implements POST /v1/peers/announce. The peer's address
// is what the server observed (X-Forwarded-For behind Caddy, else the
// connection's remote host) — peers cannot claim arbitrary addresses.
func (a *api) handlePeerAnnounce(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PeerID       string   `json:"peerId"`
		Port         int      `json:"port"`
		Capabilities []string `json:"capabilities"`
		SampleIDs    []string `json:"sampleIds"`
		TTLSeconds   int      `json:"ttlSeconds"`
	}
	if !readJSON(w, r, 256<<10, &body) {
		return
	}
	if !validPeerID(body.PeerID) {
		writeErr(w, http.StatusBadRequest, "peerId must be \"ed25519:<16 hex>\"")
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		writeErr(w, http.StatusBadRequest, "port must be 1..65535")
		return
	}
	ttl := body.TTLSeconds
	if ttl == 0 {
		ttl = peerTTLDefault
	}
	if ttl < peerTTLMin {
		ttl = peerTTLMin
	}
	if ttl > peerTTLMax {
		ttl = peerTTLMax
	}

	addr := clientAddr(r)
	now := a.now()
	caps, _ := json.Marshal(body.Capabilities)
	ids, _ := json.Marshal(body.SampleIDs)
	if body.Capabilities == nil {
		caps = []byte("[]")
	}
	if body.SampleIDs == nil {
		ids = []byte("[]")
	}
	if err := a.d.Store.AnnouncePeer(r.Context(), serverstore.PeerRow{
		PeerID:           body.PeerID,
		Addr:             addr,
		Port:             body.Port,
		CapabilitiesJSON: string(caps),
		SampleIDsJSON:    string(ids),
		AnnouncedAt:      now,
		ExpiresAt:        now.Add(time.Duration(ttl) * time.Second),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "announce failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"addr": addr, "ttlSeconds": ttl})
}

// clientAddr prefers the first X-Forwarded-For hop (set by Caddy), falling
// back to the connection's remote host.
func clientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		if addr := strings.TrimSpace(first); addr != "" {
			return addr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// handlePeersForSample implements GET /v1/peers/for-sample/{sampleId},
// excluding expired announcements.
func (a *api) handlePeersForSample(w http.ResponseWriter, r *http.Request) {
	sampleID := r.PathValue("sampleId")
	rows, err := a.d.Store.PeersForSample(r.Context(), sampleID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "peer lookup failed")
		return
	}
	type peerOut struct {
		PeerID string `json:"peerId"`
		Addr   string `json:"addr"`
		Port   int    `json:"port"`
	}
	now := a.now()
	out := []peerOut{}
	for _, p := range rows {
		if !p.ExpiresAt.After(now) {
			continue // double-guard: never hand out expired peers
		}
		out = append(out, peerOut{PeerID: p.PeerID, Addr: p.Addr, Port: p.Port})
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": out})
}
