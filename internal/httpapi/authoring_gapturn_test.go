package httpapi

// Every authoringGapEvery-th poll offers the matrix gaps first.
//
// WANTED is somebody's explicit ask and ranks first ("demand is the
// ranking"). That is right for every poll but the one this file is about:
// measured 2026-09-02, the farm completed 157 WANTED coordinates in 24 hours
// and zero EXPANSION or DEPENDENCY ones -- not because those were missing
// (the snapshot held 198 linux expansion candidates; dependency_edge named
// 2,333 child coordinates with no sample at any of them) but because a poll
// only reaches them once its WANTED-eligible candidates are exhausted, and
// at ~150 a day the 3,083-row WANTED backlog never is.
//
// A weighted round robin fixes the starvation without demoting demand: on
// one poll in authoringGapEvery, the non-WANTED candidates are offered
// first, in the order they already carry; the others stay WANTED-first. If
// that poll has no gap candidates it hands out WANTED like any other. The
// claim behind it is unchanged and ON CONFLICT DO NOTHING, so the rotation
// changes who is offered what, never who owns what.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// pollOnce issues one work poll for a fresh session and returns the kind it
// was handed ("" for NO_WORK).
func pollOnce(t *testing.T, srv string, store serverstore.AuthoringSessionStore, n int) string {
	t.Helper()
	// Same shape as the tokens the other authoring tests use: a 32-byte
	// payload, base64url without padding.
	token := "csx_author_v1_" + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%032d", n)))
	sum := sha256.Sum256([]byte(token))
	now := testNow
	if err := store.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: hex.EncodeToString(sum[:]), SessionID: fmt.Sprintf("gap-worker-%d", n), Label: fmt.Sprintf("slot%d", n),
		Model: "claude-haiku", Reasoning: "low", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv+"/v1/authoring/work/next",
		bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll %d: status %d", n, resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Work   struct {
			Kind string `json:"kind"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Work.Kind
}

func seedWanted(t *testing.T, store *snapshotStore, n int) {
	t.Helper()
	rows := make([]serverstore.WantedRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, serverstore.WantedRow{Ecosystem: "npm", Name: fmt.Sprintf("asked-%02d", i), Version: "1.0.0", Symbol: "run"})
	}
	if err := store.RecordWanted(t.Context(), testNow.Format("2006-01-02"), "0123456789abcdef", rows); err != nil {
		t.Fatal(err)
	}
}

func TestEveryFourthPollOffersTheGapsFirst(t *testing.T) {
	if authoringGapEvery < 2 {
		t.Fatalf("authoringGapEvery = %d; below 2 the rotation is either always or never", authoringGapEvery)
	}
	store := newSnapshotStore(expansionRow("gap-package"))
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Store = store })
	s := srv.URL
	seedWanted(t, store, 3*authoringGapEvery)

	var kinds []string
	for n := 1; n <= 2*authoringGapEvery; n++ {
		kinds = append(kinds, pollOnce(t, s, store, n))
	}
	for i, kind := range kinds {
		poll := i + 1
		want := "WANTED"
		if poll%authoringGapEvery == 0 {
			want = "EXPANSION"
		}
		if poll == 2*authoringGapEvery {
			// The one gap candidate was handed out on the first gap turn; the
			// second gap turn has nothing but WANTED left and hands that out.
			want = "WANTED"
		}
		if kind != want {
			t.Errorf("poll %d handed out %q, want %q (sequence so far %v)", poll, kind, want, kinds[:i+1])
		}
	}
}

// A gap turn with no gap candidates is an ordinary poll.
func TestAGapTurnWithNoGapCandidatesHandsOutWanted(t *testing.T) {
	store := newSnapshotStore() // no expansion rows at all
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Store = store })
	s := srv.URL
	seedWanted(t, store, 2*authoringGapEvery)

	for n := 1; n <= authoringGapEvery; n++ {
		if kind := pollOnce(t, s, store, n); kind != "WANTED" {
			t.Fatalf("poll %d handed out %q; with no gap candidates every poll is WANTED", n, kind)
		}
	}
}
