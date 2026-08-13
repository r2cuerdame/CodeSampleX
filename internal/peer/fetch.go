package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// ErrMiss is returned by Fetch when the artifact exists in no reachable
// source: local CAS, announced peers, and the server all came up empty.
var ErrMiss = errors.New("peer: sample artifact not found in any source")

// maxArtifactBytes caps remote reads. Canonical artifacts are ≤256KB
// compressed (plan C13); anything past this limit is hostile or corrupt
// and would fail hash verification anyway, so stop reading early.
const maxArtifactBytes = 4 << 20

// trackerPeer is one entry of GET /v1/peers/for-sample/{id} (plan C5).
type trackerPeer struct {
	PeerID string `json:"peerId"`
	Addr   string `json:"addr"`
	Port   int    `json:"port"`
}

// Fetch returns the artifact bytes for sampleID and where they came from:
// "local" (CAS hit), "peer" (another node), or "server" (Main Seeder
// fallback), in that fixed order (§15.1). Every remote payload is
// re-verified against the content id before it is trusted, cached, or
// returned; peers or servers sending wrong bytes are skipped. Peer and
// server errors never abort the chain — the next source is tried — so a
// fully offline node still serves local hits (§3.9).
func (n *Node) Fetch(ctx context.Context, sampleID string) ([]byte, string, error) {
	if !validSampleID(sampleID) {
		return nil, "", fmt.Errorf("peer: fetch: invalid sample id %q", sampleID)
	}

	// 1. Local CAS.
	if rc, err := n.CAS.Get(sampleID); err == nil {
		data, rerr := io.ReadAll(rc)
		rc.Close()
		if rerr == nil {
			if n.DB != nil {
				_ = n.DB.TouchSample(ctx, sampleID)
			}
			return data, "local", nil
		}
		// Unreadable local object: fall through and re-fetch remotely.
	}

	// 2. Peers announced to the tracker.
	self := n.Ident.PeerID()
	for _, p := range n.peersForSample(ctx, sampleID) {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if p.PeerID == self || p.Addr == "" || p.Port <= 0 {
			continue
		}
		u := "http://" + net.JoinHostPort(p.Addr, strconv.Itoa(p.Port)) +
			"/peer/v1/samples/" + sampleID
		data, err := n.httpGet(ctx, u)
		if err != nil {
			continue // peer down or refusing: try the next one
		}
		if domain.SHA256Hex(data) != sampleID {
			continue // corrupt or malicious payload: reject, keep going
		}
		n.storeFetched(ctx, sampleID, data)
		return data, "peer", nil
	}

	// 3. Server artifact endpoint.
	if data, err := n.httpGet(ctx, n.serverURL()+"/v1/samples/"+sampleID+"/artifact"); err == nil {
		if domain.SHA256Hex(data) == sampleID {
			n.storeFetched(ctx, sampleID, data)
			return data, "server", nil
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return nil, "", ErrMiss
}

// SeedHot warms the cache with network-HOT samples (§15.3): each id not
// yet cached is fetched while total CAS usage stays under budgetMB. It
// stops at the budget and NEVER evicts — eviction belongs to the cache
// policy, not the seeder. Individual fetch misses/errors skip to the next
// id. Returns how many samples were newly seeded.
func (n *Node) SeedHot(ctx context.Context, hotIDs []string, budgetMB int) (int, error) {
	budget := int64(budgetMB) << 20
	seeded := 0
	for _, id := range hotIDs {
		if err := ctx.Err(); err != nil {
			return seeded, err
		}
		if !validSampleID(id) || n.CAS.Has(id) {
			continue
		}
		used, err := n.CAS.TotalSize()
		if err != nil {
			return seeded, fmt.Errorf("peer: seed: %w", err)
		}
		if used >= budget {
			break
		}
		if _, _, err := n.Fetch(ctx, id); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return seeded, ctxErr
			}
			continue // miss or transient failure: try the next hot id
		}
		seeded++
	}
	return seeded, nil
}

// peersForSample asks the tracker who has sampleID. Any failure (server
// down, bad JSON) yields an empty list so Fetch falls through to the
// server source.
func (n *Node) peersForSample(ctx context.Context, sampleID string) []trackerPeer {
	data, err := n.httpGet(ctx, n.serverURL()+"/v1/peers/for-sample/"+sampleID)
	if err != nil {
		return nil
	}
	var out struct {
		Peers []trackerPeer `json:"peers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out.Peers
}

// httpGet fetches url with a bounded read; non-200 statuses are errors.
func (n *Node) httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := n.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("peer: get %s: status %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxArtifactBytes {
		return nil, fmt.Errorf("peer: get %s: body exceeds %d bytes", url, maxArtifactBytes)
	}
	return data, nil
}

// storeFetched caches verified artifact bytes in the CAS and marks the
// sample row has_artifact so the next announce advertises it. Caching is
// best-effort: a storage failure never fails the fetch that already
// verified the bytes.
func (n *Node) storeFetched(ctx context.Context, sampleID string, data []byte) {
	if _, err := n.CAS.Put(bytes.NewReader(data)); err != nil {
		return
	}
	if n.DB == nil {
		return
	}
	row, ok, err := n.DB.GetSample(ctx, sampleID)
	if err != nil {
		return
	}
	if !ok {
		row = localdb.SampleRow{
			SampleID:     sampleID,
			ManifestJSON: "{}", // metadata arrives with shard sync; artifact is authoritative by content id
			Status:       "PUBLISHED",
			CreatedAt:    time.Now(),
		}
	}
	row.HasArtifact = true
	row.LastUsed = time.Now()
	_ = n.DB.SaveSample(ctx, row)
}
