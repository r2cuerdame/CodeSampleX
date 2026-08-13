package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
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

	// A tracker full of unreachable peers is worse than an empty one: every
	// fetch then pays a connection attempt per listed peer before falling
	// back. Most developer machines sit behind NAT, so the observed address
	// usually cannot be dialed back. Verify before publishing.
	if !a.peerReachable(r.Context(), addr, body.Port) {
		writeJSON(w, http.StatusOK, map[string]any{
			"addr":       addr,
			"registered": false,
			"reason": "this address is not reachable from the server, so other peers could not " +
				"fetch from it; evidence and sample uploads are unaffected",
		})
		return
	}

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
	writeJSON(w, http.StatusOK, map[string]any{"addr": addr, "ttlSeconds": ttl, "registered": true})
}

// peerReachabilityTimeout bounds the dial-back. It is short on purpose:
// announce is a routine background call and must not block on a peer
// whose port is filtered rather than refused.
const peerReachabilityTimeout = 3 * time.Second

// peerReachable dials the announced address back and asks for the peer
// ping endpoint. PeerProbe is overridable so tests never touch a network.
func (a *api) peerReachable(ctx context.Context, addr string, port int) bool {
	probe := a.d.PeerProbe
	if probe == nil {
		probe = defaultPeerProbe
	}
	pctx, cancel := context.WithTimeout(ctx, peerReachabilityTimeout)
	defer cancel()
	return probe(pctx, addr, port)
}

func defaultPeerProbe(ctx context.Context, addr string, port int) bool {
	url := "http://" + net.JoinHostPort(addr, strconv.Itoa(port)) + "/peer/v1/ping"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: peerReachabilityTimeout}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024)) //nolint:errcheck
	return resp.StatusCode == http.StatusOK
}

// clientAddr returns the address the deployment's own proxy observed.
//
// Caddy APPENDS the connecting IP to any X-Forwarded-For the client sent,
// so the RIGHTMOST hop is the one hop a client cannot forge; the leftmost
// is whatever the client typed. That distinction matters twice here: the
// tracker publishes this address to other peers, and the server dials it
// back to check reachability — trusting the left hop would let a caller
// point both at an arbitrary host.
func clientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.LastIndex(xff, ","); i >= 0 {
			xff = xff[i+1:]
		}
		if addr := strings.TrimSpace(xff); addr != "" {
			return addr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// hotShardLimit is how many shard keys a client is told to warm. Each is
// one small HTTP GET; the point is that a fresh install has something
// cached before its first search, not that it mirrors the network.
const hotShardLimit = 25

// withHotShards adds the "hotShards" key to a stats document. Clients read
// it to decide what to warm (daemon.fetchHot), and a fresh install has no
// local package history to warm from — without this key its cache stays
// empty and every search answers "no cached data". A failure here degrades
// to the stats document unchanged rather than failing the request.
func (a *api) withHotShards(ctx context.Context, statsJSON string) string {
	keys, err := a.d.Store.HotShardKeys(ctx, hotShardLimit)
	if err != nil || len(keys) == 0 {
		return statsJSON
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(statsJSON), &doc); err != nil {
		return statsJSON
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return statsJSON
	}
	doc["hotShards"] = raw
	merged, err := json.Marshal(doc)
	if err != nil {
		return statsJSON
	}
	return string(merged)
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
