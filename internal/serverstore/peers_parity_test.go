package serverstore

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func runPeersForSampleParity(t *testing.T, store Store, setAnnouncedAt func(*testing.T, string, time.Time)) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	newer := now.Add(-time.Minute)
	older := now.Add(-time.Hour)
	expired := now.Add(-time.Minute)
	const sampleID = "sha256:peers-for-sample-parity"
	peers := []PeerRow{
		{PeerID: "ed25519:0000000000000004", Addr: "203.0.113.4", Port: 4, SampleIDsJSON: `["sha256:peers-for-sample-parity"]`, ExpiresAt: now.Add(time.Hour)},
		{PeerID: "ed25519:0000000000000002", Addr: "203.0.113.2", Port: 2, SampleIDsJSON: `["sha256:peers-for-sample-parity"]`, ExpiresAt: now.Add(time.Hour)},
		{PeerID: "ed25519:0000000000000003", Addr: "203.0.113.3", Port: 3, SampleIDsJSON: `["sha256:peers-for-sample-parity"]`, ExpiresAt: expired},
		{PeerID: "ed25519:0000000000000001", Addr: "203.0.113.1", Port: 1, SampleIDsJSON: `["sha256:peers-for-sample-parity"]`, ExpiresAt: now.Add(time.Hour)},
	}
	announced := map[string]time.Time{
		peers[0].PeerID: newer,
		peers[1].PeerID: newer,
		peers[2].PeerID: now,
		peers[3].PeerID: older,
	}
	for _, peer := range peers {
		if err := store.AnnouncePeer(ctx, peer); err != nil {
			t.Fatalf("AnnouncePeer(%s): %v", peer.PeerID, err)
		}
		if setAnnouncedAt != nil {
			setAnnouncedAt(t, peer.PeerID, announced[peer.PeerID])
		}
	}
	got, err := store.PeersForSample(ctx, sampleID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, peer := range got {
		ids = append(ids, peer.PeerID)
	}
	want := []string{peers[1].PeerID, peers[0].PeerID, peers[3].PeerID}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("PeersForSample peer IDs = %v, want announced_at DESC, peer_id ASC %v", ids, want)
	}
}

func TestPeersForSampleParity(t *testing.T) {
	fake := NewFake()
	runPeersForSampleParity(t, fake, func(t *testing.T, peerID string, at time.Time) {
		t.Helper()
		fake.mu.Lock()
		defer fake.mu.Unlock()
		peer := fake.peers[peerID]
		peer.AnnouncedAt = at
		fake.peers[peerID] = peer
	})
}

func TestIntegrationPGPeersForSampleParity(t *testing.T) {
	pg := openTestPG(t)
	runPeersForSampleParity(t, pg, func(t *testing.T, peerID string, at time.Time) {
		t.Helper()
		if err := pg.withConn(context.Background(), func(c *pgx.Conn) error {
			_, err := c.Exec(context.Background(), `UPDATE peers SET announced_at=$1 WHERE peer_id=$2`, at, peerID)
			return err
		}); err != nil {
			t.Fatalf("set announced_at for %s: %v", peerID, err)
		}
	})
}
