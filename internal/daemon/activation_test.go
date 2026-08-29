package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// S3 of the funnel is "first sync complete". The shards table said a sync had
// happened but not when — docs/activation-funnel.md §1 records it as a local
// fact with no timestamp — so nothing could tell an install that synced on
// day one from one that finally reached the network a month later.
//
// The stamp is tied to a SUCCESSFUL warm for the same reason WarmedKeys is:
// an offline sync returns cleanly, and treating it as "synced" would mark the
// stage reached on a machine whose shard cache is still empty.
func TestOnlyASyncThatActuallyWarmedAShardStampsTheFirstSync(t *testing.T) {
	shard := `{"schemaVersion":1,"key":"npm/axios/1","generatedAt":"2026-08-13T00:00:00Z","packages":[]}`
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"hotShards":[]}`)
	})
	mux.HandleFunc("GET /v1/shards/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "axios") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"e1"`)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, shard)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = ts.URL
	})
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	// Nothing public in the inventory yet: the warm list is empty, so this
	// sync succeeds without warming anything.
	if res := d.SyncNow(ctx); res.WarmedKeys != 0 {
		t.Fatalf("warmed %d keys with an empty inventory", res.WarmedKeys)
	}
	led, err := d.DB.ActivationLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !led.FirstSyncAt.IsZero() {
		t.Fatalf("a sync that warmed nothing stamped firstSyncAt = %s", led.FirstSyncAt)
	}

	purl := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	if err := d.DB.SetPublicness(ctx, purl, "PUBLIC"); err != nil {
		t.Fatalf("set publicness: %v", err)
	}
	res := d.SyncNow(ctx)
	if res.WarmedKeys == 0 {
		t.Fatalf("sync warmed no keys: %+v", res)
	}
	if led, err = d.DB.ActivationLedger(ctx); err != nil {
		t.Fatal(err)
	}
	if led.FirstSyncAt.IsZero() {
		t.Fatal("a sync that warmed a shard recorded no firstSyncAt")
	}
}

// Local-only mode is where this stamp could do the most damage: SyncNow is a
// complete no-op there, and a ledger entry claiming a sync happened would be
// the local record contradicting the mode's own promise.
func TestLocalOnlySyncStampsNothing(t *testing.T) {
	home := newTestHome(t, func(cfg *config.Config) { cfg.Mode = config.ModeLocalOnly })
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()
	d.SyncNow(ctx)
	led, err := d.DB.ActivationLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !led.FirstSyncAt.IsZero() {
		t.Fatalf("local-only sync stamped firstSyncAt = %s", led.FirstSyncAt)
	}
	if _, ok, err := d.DB.GetStat(ctx, localdb.StatFirstSyncAt); err != nil || ok {
		t.Fatalf("local-only sync wrote the firstSyncAt key (ok=%v err=%v)", ok, err)
	}
}

// The ledger only helps if something reads it back. The local stats document
// is what `csx stats`, `csx ui` and get_local_stats all render, so the stamps
// travel with it — and the S2→S6 duration (§5) is computed here rather than
// by three separate consumers each getting the endpoints slightly wrong.
func TestTheLocalStatsDocumentCarriesTheActivationLedger(t *testing.T) {
	home := newTestHome(t, nil)
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	st, err := d.StatsNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Readiness.InitAt != "" || st.Readiness.FirstHitAt != "" {
		t.Fatalf("a fresh install already claims stages: %+v", st.Readiness)
	}
	// §6: a thing not measured renders as a gap, never as a zero. A JSON
	// consumer must be able to tell "no answer yet" from "answered instantly".
	if st.Readiness.SecondsToFirstAnswer != nil {
		t.Fatalf("secondsToFirstAnswer = %d with no first answer", *st.Readiness.SecondsToFirstAnswer)
	}

	initAt := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	if err := d.DB.StampFirst(ctx, localdb.StatInitAt, initAt); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.StampFirst(ctx, localdb.StatFirstHitAt, initAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st, err = d.StatsNow(ctx); err != nil {
		t.Fatal(err)
	}
	if st.Readiness.InitAt != "2026-08-20T09:00:00Z" {
		t.Errorf("initAt = %q", st.Readiness.InitAt)
	}
	if st.Readiness.SecondsToFirstAnswer == nil || *st.Readiness.SecondsToFirstAnswer != 7200 {
		t.Errorf("secondsToFirstAnswer = %v, want 7200", st.Readiness.SecondsToFirstAnswer)
	}
}

// The whole reason the funnel is local (docs/activation-funnel.md §2.1, §2.3)
// is that S1 and S2 happen before `csx init` asks the mode question, so a
// stamp on the wire would be collection before consent — and in local-only
// mode it would break the one promise that mode exists to make.
//
// This measures it rather than asserting it: a community daemon with every
// stamp set and real work queued, against a server that keeps every byte it
// is sent.
func TestNoActivationStampReachesTheWire(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.URL.Path+" "+string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = srv.URL
	})
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	// A timestamp no other field could produce, so a match is proof and not
	// a coincidence with an epoch or a generatedAt.
	const sentinel = "2031-02-03T04:05:06Z"
	stamp, err := time.Parse(time.RFC3339, sentinel)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range localdb.FirstStampKeys {
		if err := d.DB.StampFirst(ctx, key, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.DB.Stamp(ctx, localdb.StatMCPLastReadyAt, stamp); err != nil {
		t.Fatal(err)
	}

	// Something real to upload, so the request bodies are not empty for a
	// reason that has nothing to do with the ledger.
	env := testEnv()
	if err := d.DB.SaveEnvironment(ctx, env); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.RecordObservation(ctx, localdb.ObsKey{
		Epoch: "2026-08-13", PURL: "pkg:npm/axios@1.12.0",
		EnvHash: env.Hash(), Stage: domain.StageProjectCompile, Result: domain.ResultPass,
	}, 2); err != nil {
		t.Fatal(err)
	}
	d.SyncNow(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("the community sync sent nothing at all; this test would pass vacuously")
	}
	for _, body := range bodies {
		if strings.Contains(body, sentinel) {
			t.Errorf("an activation stamp value reached the wire: %s", body)
		}
		for _, key := range append(append([]string{}, localdb.FirstStampKeys...), localdb.StatMCPLastReadyAt) {
			if strings.Contains(body, key) {
				t.Errorf("activation key %q reached the wire: %s", key, body)
			}
		}
	}
}

// §7 puts the readiness rows at the top of csx ui, and the panel is the
// answer a user and a support conversation actually need: which stage this
// install is at, where each state was read from, and what to run next. The
// header has to say the panel is local, because "a new panel of stages" is
// exactly the shape of a thing a privacy-minded reader assumes is telemetry.
func TestTheDashboardShowsTheReadinessPanelBeforeAnythingElse(t *testing.T) {
	home := newTestHome(t, nil)
	d, _ := startDaemon(t, home)
	ctx := context.Background()

	initAt := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	if err := d.DB.StampFirst(ctx, localdb.StatInitAt, initAt); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.StampFirst(ctx, localdb.StatFirstHitAt, initAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(d.BaseURL() + "/ui")
	if err != nil {
		t.Fatalf("GET /ui: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)

	for _, want := range []string{
		"Readiness",
		"nothing on this panel is uploaded",
		"2026-08-20T11:00:00Z",
		"2h0m0s after csx init",
		"restart your coding agent",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard is missing %q", want)
		}
	}
	if idx, community := strings.Index(page, "Readiness"), strings.Index(page, "Community status"); idx < 0 || idx > community {
		t.Errorf("readiness panel is at %d, after community status at %d", idx, community)
	}
	// An unreached stage is a gap. A zero time rendered as a date would read
	// as a measurement of something that never happened.
	if strings.Contains(page, "0001-01-01") || strings.Contains(page, "1970-01-01") {
		t.Error("the dashboard rendered a zero time for an unreached stage")
	}
	// §7: it is the NOT-ready row that carries the command. The first cut
	// printed "→ run csx init" beside an install that had already been
	// initialized two hours before its first answer, which reads as an
	// instruction to redo the one step that worked.
	if strings.Contains(page, "run csx init") {
		t.Error("a stage that was already reached still carries its next action")
	}
}
