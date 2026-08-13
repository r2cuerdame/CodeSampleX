// Package peer implements the local P2P node (plan C15, goal.md §15):
// announcing cached sample artifacts to the central tracker, serving them
// to other peers over HTTP, and fetching artifacts with the fallback order
// local CAS → tracker peers → server → MISS. Announce payloads carry ONLY
// the rotating-free persistent peer id, the listen port, capability tags,
// and content-addressed sample ids — never paths, project names, or any
// other machine-identifying detail (§2.2).
package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// announceTTLSeconds is the tracker registration TTL (30 min); the loop
// re-announces every announceInterval (10 min) so healthy peers never expire.
const (
	announceTTLSeconds = 1800
	announceInterval   = 10 * time.Minute
)

// Node is one peer: a CAS full of verified sample artifacts, the local DB
// tracking which samples have artifacts, and the identity that names this
// peer on the tracker.
type Node struct {
	CAS       *cas.Store
	DB        *localdb.DB
	Ident     *identity.Identity
	HTTP      *http.Client
	ServerURL string
	Port      int

	// announceEvery overrides the 10-minute announce cadence in tests.
	announceEvery time.Duration

	mu           sync.Mutex
	lastAnnounce AnnounceResult
}

// AnnounceResult is what the tracker made of the last announce. Registered
// false means the tracker could not dial this peer back, so it will not be
// handed to other peers — normal behind NAT, and harmless: evidence,
// uploads, and fetching all still work, this peer just cannot serve.
type AnnounceResult struct {
	Done       bool
	Addr       string `json:"addr"`
	Registered bool   `json:"registered"`
	Reason     string `json:"reason"`
}

// LastAnnounce reports the tracker's verdict on the most recent announce.
// Done is false until the first announce completes.
func (n *Node) LastAnnounce() AnnounceResult {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastAnnounce
}

// announcePayload is the exact tracker wire format (plan C5,
// POST /v1/peers/announce). Adding a field here is a privacy decision:
// nothing beyond these five keys may ever be sent.
type announcePayload struct {
	PeerID       string   `json:"peerId"`
	Port         int      `json:"port"`
	Capabilities []string `json:"capabilities"`
	SampleIDs    []string `json:"sampleIds"`
	TTLSeconds   int      `json:"ttlSeconds"`
}

func (n *Node) client() *http.Client {
	if n.HTTP != nil {
		return n.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (n *Node) serverURL() string {
	u := n.ServerURL
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}

// Announce registers this peer with the tracker: peer id, listen port,
// blob capability, and the content-addressed ids of samples whose
// artifacts are locally cached. Sample ids are SHA-256 digests, so the
// payload can never carry paths or project names.
func (n *Node) Announce(ctx context.Context) error {
	ids := []string{}
	if n.DB != nil {
		rows, err := n.DB.ListSamples(ctx)
		if err != nil {
			return fmt.Errorf("peer: announce: list samples: %w", err)
		}
		for _, r := range rows {
			if r.HasArtifact {
				ids = append(ids, r.SampleID)
			}
		}
	}
	body, err := json.Marshal(announcePayload{
		PeerID:       n.Ident.PeerID(),
		Port:         n.Port,
		Capabilities: []string{"blob"},
		SampleIDs:    ids,
		TTLSeconds:   announceTTLSeconds,
	})
	if err != nil {
		return fmt.Errorf("peer: announce: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		n.serverURL()+"/v1/peers/announce", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("peer: announce: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client().Do(req)
	if err != nil {
		return fmt.Errorf("peer: announce: %w", err)
	}
	defer resp.Body.Close()
	replyBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("peer: announce: tracker status %d", resp.StatusCode)
	}

	// Older servers reply without "registered"; absence means accepted.
	res := AnnounceResult{Done: true, Registered: true}
	_ = json.Unmarshal(replyBody, &res)
	n.mu.Lock()
	n.lastAnnounce = res
	n.mu.Unlock()
	return nil
}

// StartAnnouncing launches the background announce loop: one immediate
// announce, then one every 10 minutes until ctx is canceled. Announce
// failures (server down, §3.9) are tolerated silently — the next tick
// retries; local serving and fetching are unaffected.
func (n *Node) StartAnnouncing(ctx context.Context) {
	interval := n.announceEvery
	if interval <= 0 {
		interval = announceInterval
	}
	go func() {
		announce := func() {
			before := n.LastAnnounce()
			if err := n.Announce(ctx); err != nil {
				return
			}
			// Log the refusal once per transition, not every 10 minutes:
			// most developer machines are behind NAT and this is expected,
			// but a peer that meant to serve should hear about it.
			if now := n.LastAnnounce(); !now.Registered && (!before.Done || before.Registered) {
				log.Printf("csx peer: tracker will not list this node: %s", now.Reason)
			}
		}
		announce()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				announce()
			}
		}
	}()
}
