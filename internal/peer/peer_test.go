package peer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// newNode builds a Node with isolated temp state pointed at serverURL.
func newNode(t *testing.T, serverURL string) *Node {
	t.Helper()
	home := t.TempDir()
	store, err := cas.Open(filepath.Join(home, "cas"))
	if err != nil {
		t.Fatalf("cas.Open: %v", err)
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("localdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ident, err := identity.LoadOrCreate(home)
	if err != nil {
		t.Fatalf("identity.LoadOrCreate: %v", err)
	}
	return &Node{
		CAS:       store,
		DB:        db,
		Ident:     ident,
		HTTP:      &http.Client{Timeout: 5 * time.Second},
		ServerURL: serverURL,
		Port:      48620,
	}
}

// putArtifact stores data in the node's CAS and marks has_artifact, the
// state a seeder node is in. Returns the sample id.
func putArtifact(t *testing.T, n *Node, data []byte) string {
	t.Helper()
	id, err := n.CAS.Put(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("cas.Put: %v", err)
	}
	err = n.DB.SaveSample(context.Background(), localdb.SampleRow{
		SampleID: id, ManifestJSON: "{}", Status: "PUBLISHED", HasArtifact: true,
	})
	if err != nil {
		t.Fatalf("SaveSample: %v", err)
	}
	return id
}

// servePeer exposes n's peer handler on an httptest server and rewrites
// n.Port so its announce payload advertises the real listening port.
func servePeer(t *testing.T, n *Node) (host string, port int) {
	t.Helper()
	srv := httptest.NewServer(n.Handler())
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse peer url: %v", err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split peer host: %v", err)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("peer port: %v", err)
	}
	n.Port = port
	return host, port
}

// fakeTracker is an in-process stand-in for the central server: it accepts
// announces, answers for-sample lookups from them, and serves artifacts
// (the Main Seeder fallback).
type fakeTracker struct {
	mu           sync.Mutex
	announces    [][]byte // raw announce bodies, for payload assertions
	entries      map[string]trackerFakeEntry
	artifacts    map[string][]byte
	artifactHits int
}

type trackerFakeEntry struct {
	addr string
	port int
	ids  []string
}

func newTracker(t *testing.T) (*fakeTracker, string) {
	t.Helper()
	ft := &fakeTracker{
		entries:   map[string]trackerFakeEntry{},
		artifacts: map[string][]byte{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/peers/announce", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p struct {
			PeerID    string   `json:"peerId"`
			Port      int      `json:"port"`
			SampleIDs []string `json:"sampleIds"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ft.mu.Lock()
		ft.announces = append(ft.announces, body)
		ft.entries[p.PeerID] = trackerFakeEntry{addr: host, port: p.Port, ids: p.SampleIDs}
		ft.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"addr": host})
	})
	mux.HandleFunc("GET /v1/peers/for-sample/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		peers := []map[string]any{}
		ft.mu.Lock()
		for peerID, e := range ft.entries {
			for _, sid := range e.ids {
				if sid == id {
					peers = append(peers, map[string]any{"peerId": peerID, "addr": e.addr, "port": e.port})
					break
				}
			}
		}
		ft.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"peers": peers})
	})
	mux.HandleFunc("GET /v1/samples/{id}/artifact", func(w http.ResponseWriter, r *http.Request) {
		ft.mu.Lock()
		ft.artifactHits++
		data, ok := ft.artifacts[r.PathValue("id")]
		ft.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(data)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return ft, srv.URL
}

func (ft *fakeTracker) serverArtifactHits() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.artifactHits
}

// deadServerURL returns a URL nothing listens on.
func deadServerURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

func TestServeHandler(t *testing.T) {
	a := newNode(t, deadServerURL(t))
	data := []byte("artifact-bytes-ping-test")
	id := putArtifact(t, a, data)
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)

	// ping
	resp, err := http.Get(srv.URL + "/peer/v1/ping")
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "csx-peer" {
		t.Fatalf("ping = %d %q, want 200 %q", resp.StatusCode, body, "csx-peer")
	}

	// present artifact
	resp, err = http.Get(srv.URL + "/peer/v1/samples/" + id)
	if err != nil {
		t.Fatalf("get sample: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != string(data) {
		t.Fatalf("get sample = %d, body mismatch", resp.StatusCode)
	}

	// absent artifact
	absent := domain.SHA256Hex([]byte("not-stored"))
	resp, err = http.Get(srv.URL + "/peer/v1/samples/" + absent)
	if err != nil {
		t.Fatalf("get absent: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("absent sample = %d, want 404", resp.StatusCode)
	}

	// malformed id
	resp, err = http.Get(srv.URL + "/peer/v1/samples/not-a-valid-id")
	if err != nil {
		t.Fatalf("get malformed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("malformed id = %d, want 404", resp.StatusCode)
	}
}

func TestFetchLocalWithServerUnreachable(t *testing.T) {
	// ServerURL points at a dead server: local CAS path must still work (§3.9).
	n := newNode(t, deadServerURL(t))
	data := []byte("locally cached artifact")
	id := putArtifact(t, n, data)

	got, source, err := n.Fetch(context.Background(), id)
	if err != nil {
		t.Fatalf("Fetch local: %v", err)
	}
	if source != "local" {
		t.Fatalf("source = %q, want local", source)
	}
	if string(got) != string(data) {
		t.Fatalf("bytes mismatch")
	}
}

func TestFetchFromPeerViaAnnounce(t *testing.T) {
	ft, trackerURL := newTracker(t)
	ctx := context.Background()

	a := newNode(t, trackerURL)
	data := []byte("shared artifact between peers")
	id := putArtifact(t, a, data)
	servePeer(t, a)
	if err := a.Announce(ctx); err != nil {
		t.Fatalf("A announce: %v", err)
	}

	b := newNode(t, trackerURL)
	got, source, err := b.Fetch(ctx, id)
	if err != nil {
		t.Fatalf("B fetch: %v", err)
	}
	if source != "peer" {
		t.Fatalf("source = %q, want peer", source)
	}
	if string(got) != string(data) {
		t.Fatalf("bytes mismatch")
	}
	if ft.serverArtifactHits() != 0 {
		t.Fatalf("server artifact endpoint hit %d times, want 0", ft.serverArtifactHits())
	}
	// stored into CAS + has_artifact marked
	if !b.CAS.Has(id) {
		t.Fatalf("B CAS missing fetched artifact")
	}
	row, ok, err := b.DB.GetSample(ctx, id)
	if err != nil || !ok {
		t.Fatalf("B sample row: ok=%v err=%v", ok, err)
	}
	if !row.HasArtifact {
		t.Fatalf("B sample row has_artifact not set")
	}
	// second fetch is a local hit
	_, source, err = b.Fetch(ctx, id)
	if err != nil || source != "local" {
		t.Fatalf("second fetch = %q/%v, want local/nil", source, err)
	}
}

func TestFetchCorruptPeerFallsBackToServer(t *testing.T) {
	ft, trackerURL := newTracker(t)
	ctx := context.Background()

	data := []byte("the genuine artifact")
	id := domain.SHA256Hex(data)

	// Malicious/corrupt peer serving wrong bytes under the right id.
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("corrupted bytes, hash will not match"))
	}))
	t.Cleanup(evil.Close)
	eu, _ := url.Parse(evil.URL)
	host, portStr, _ := net.SplitHostPort(eu.Host)
	port, _ := strconv.Atoi(portStr)
	ft.mu.Lock()
	ft.entries["ed25519:evilpeer00000000"] = trackerFakeEntry{addr: host, port: port, ids: []string{id}}
	ft.artifacts[id] = data
	ft.mu.Unlock()

	b := newNode(t, trackerURL)
	got, source, err := b.Fetch(ctx, id)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if source != "server" {
		t.Fatalf("source = %q, want server (corrupt peer rejected)", source)
	}
	if string(got) != string(data) {
		t.Fatalf("bytes mismatch")
	}
	if !b.CAS.Has(id) {
		t.Fatalf("verified artifact not cached")
	}
}

func TestFetchServerFallbackZeroPeers(t *testing.T) {
	ft, trackerURL := newTracker(t)
	data := []byte("only the server has this one")
	id := domain.SHA256Hex(data)
	ft.mu.Lock()
	ft.artifacts[id] = data
	ft.mu.Unlock()

	b := newNode(t, trackerURL)
	got, source, err := b.Fetch(context.Background(), id)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if source != "server" || string(got) != string(data) {
		t.Fatalf("source=%q, want server with matching bytes", source)
	}
	row, ok, _ := b.DB.GetSample(context.Background(), id)
	if !ok || !row.HasArtifact {
		t.Fatalf("has_artifact not marked after server fetch")
	}
}

func TestFetchMissEverywhere(t *testing.T) {
	_, trackerURL := newTracker(t)
	b := newNode(t, trackerURL)
	id := domain.SHA256Hex([]byte("nobody has this"))
	_, _, err := b.Fetch(context.Background(), id)
	if !errors.Is(err, ErrMiss) {
		t.Fatalf("err = %v, want ErrMiss", err)
	}

	// Same with the server fully down: still ErrMiss, no panic.
	down := newNode(t, deadServerURL(t))
	_, _, err = down.Fetch(context.Background(), id)
	if !errors.Is(err, ErrMiss) {
		t.Fatalf("server-down err = %v, want ErrMiss", err)
	}
}

func TestFetchInvalidID(t *testing.T) {
	b := newNode(t, deadServerURL(t))
	_, _, err := b.Fetch(context.Background(), "../../etc/passwd")
	if err == nil || errors.Is(err, ErrMiss) {
		t.Fatalf("invalid id err = %v, want non-miss error", err)
	}
}

func TestSeedHotRespectsBudget(t *testing.T) {
	ft, trackerURL := newTracker(t)
	ctx := context.Background()

	// Three ~600KB artifacts: with a 1MB budget only two fit before the
	// budget gate stops seeding.
	var ids []string
	for i := 0; i < 3; i++ {
		data := make([]byte, 600*1024)
		for j := range data {
			data[j] = byte(i + j%251)
		}
		id := domain.SHA256Hex(data)
		ft.mu.Lock()
		ft.artifacts[id] = data
		ft.mu.Unlock()
		ids = append(ids, id)
	}

	b := newNode(t, trackerURL)
	seeded, err := b.SeedHot(ctx, ids, 1)
	if err != nil {
		t.Fatalf("SeedHot: %v", err)
	}
	if seeded != 2 {
		t.Fatalf("seeded = %d, want 2 (budget stop)", seeded)
	}
	if b.CAS.Has(ids[2]) {
		t.Fatalf("third artifact fetched past budget")
	}

	// Zero budget seeds nothing.
	c := newNode(t, trackerURL)
	seeded, err = c.SeedHot(ctx, ids, 0)
	if err != nil || seeded != 0 {
		t.Fatalf("zero budget: seeded=%d err=%v, want 0/nil", seeded, err)
	}

	// Already-cached ids are skipped without counting.
	seeded, err = b.SeedHot(ctx, ids[:2], 10)
	if err != nil || seeded != 0 {
		t.Fatalf("cached skip: seeded=%d err=%v, want 0/nil", seeded, err)
	}
}

func TestAnnouncePayloadPrivacy(t *testing.T) {
	ft, trackerURL := newTracker(t)
	ctx := context.Background()

	a := newNode(t, trackerURL)
	// Sample WITH artifact; its manifest deliberately contains a path-like
	// string that must never appear in the announce payload.
	withData := []byte("announced artifact")
	withID := putArtifact(t, a, withData)
	err := a.DB.SaveSample(ctx, localdb.SampleRow{
		SampleID:     withID,
		ManifestJSON: `{"secretPath":"C:\\Users\\alice\\my-company-project\\src"}`,
		Status:       "PUBLISHED",
		HasArtifact:  true,
	})
	if err != nil {
		t.Fatalf("SaveSample: %v", err)
	}
	// Sample WITHOUT artifact: must not be announced.
	noArtifactID := domain.SHA256Hex([]byte("metadata only"))
	err = a.DB.SaveSample(ctx, localdb.SampleRow{
		SampleID: noArtifactID, ManifestJSON: "{}", Status: "PUBLISHED", HasArtifact: false,
	})
	if err != nil {
		t.Fatalf("SaveSample: %v", err)
	}

	a.Port = 48620
	if err := a.Announce(ctx); err != nil {
		t.Fatalf("Announce: %v", err)
	}

	ft.mu.Lock()
	if len(ft.announces) != 1 {
		ft.mu.Unlock()
		t.Fatalf("announces = %d, want 1", len(ft.announces))
	}
	raw := ft.announces[0]
	ft.mu.Unlock()

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	// Exactly the five contract fields — nothing else can leak.
	wantKeys := map[string]bool{
		"peerId": true, "port": true, "capabilities": true, "sampleIds": true, "ttlSeconds": true,
	}
	for k := range payload {
		if !wantKeys[k] {
			t.Fatalf("unexpected announce field %q", k)
		}
	}
	for k := range wantKeys {
		if _, ok := payload[k]; !ok {
			t.Fatalf("missing announce field %q", k)
		}
	}
	if payload["peerId"] != a.Ident.PeerID() {
		t.Fatalf("peerId = %v", payload["peerId"])
	}
	if payload["ttlSeconds"] != float64(1800) {
		t.Fatalf("ttlSeconds = %v, want 1800", payload["ttlSeconds"])
	}
	caps, _ := payload["capabilities"].([]any)
	if len(caps) != 1 || caps[0] != "blob" {
		t.Fatalf("capabilities = %v, want [blob]", payload["capabilities"])
	}
	sids, _ := payload["sampleIds"].([]any)
	if len(sids) != 1 || sids[0] != withID {
		t.Fatalf("sampleIds = %v, want exactly [%s]", payload["sampleIds"], withID)
	}
	// No path separators, drive letters, usernames, or project names anywhere.
	s := string(raw)
	for _, bad := range []string{"\\", "/", "alice", "my-company-project", "Users", ".go", ".ts"} {
		if strings.Contains(s, bad) {
			t.Fatalf("announce payload leaks %q: %s", bad, s)
		}
	}
}

func TestStartAnnouncingLoopAndStop(t *testing.T) {
	ft, trackerURL := newTracker(t)
	a := newNode(t, trackerURL)
	a.announceEvery = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	a.StartAnnouncing(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for {
		ft.mu.Lock()
		n := len(ft.announces)
		ft.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("announce loop produced %d announces, want >=2", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	time.Sleep(60 * time.Millisecond)
	ft.mu.Lock()
	after := len(ft.announces)
	ft.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	ft.mu.Lock()
	final := len(ft.announces)
	ft.mu.Unlock()
	if final > after+1 { // at most one in-flight announce may land after cancel
		t.Fatalf("announce loop kept running after cancel: %d -> %d", after, final)
	}
}

func TestAnnounceServerDownDoesNotPanic(t *testing.T) {
	a := newNode(t, deadServerURL(t))
	if err := a.Announce(context.Background()); err == nil {
		t.Fatalf("announce to dead server: want error, got nil")
	}
}

func TestListenAndServeGracefulShutdown(t *testing.T) {
	a := newNode(t, deadServerURL(t))
	// Pick a free port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	a.Port = port
	// Loopback only. This test talks to 127.0.0.1 and nothing else, and
	// binding every interface made Windows Defender Firewall prompt on
	// every run: the test binary lands in a new temp path each time, so it
	// is a new program to the firewall each time.
	a.BindAddr = "127.0.0.1"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.ListenAndServe(ctx) }()

	// Wait for the listener to come up, then ping it.
	var pinged bool
	for i := 0; i < 100; i++ {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/peer/v1/ping")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && string(body) == "csx-peer" {
				pinged = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !pinged {
		t.Fatalf("peer listener never answered ping")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("ListenAndServe did not shut down after cancel")
	}
}

// A real peer must bind every interface — the tracker dials it back and
// other peers fetch from it — so the default has to stay the wildcard.
// BindAddr exists so a TEST can be loopback-only, which is what stops
// Windows Defender Firewall prompting on every run.
func TestListenAddrDefaultsToEveryInterface(t *testing.T) {
	n := &Node{Port: 41234}
	if got := n.listenAddr(); got != ":41234" {
		t.Errorf("default listenAddr = %q, want %q", got, ":41234")
	}
	n.BindAddr = "127.0.0.1"
	if got := n.listenAddr(); got != "127.0.0.1:41234" {
		t.Errorf("bound listenAddr = %q, want %q", got, "127.0.0.1:41234")
	}
}
