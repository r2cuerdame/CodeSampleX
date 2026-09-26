package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestAuthoringWindowSkipsSessionBarredCandidates(t *testing.T) {
	rows := make([]serverstore.WantedRow, 0, maxOfferedCandidates+1)
	for i := 0; i < maxOfferedCandidates; i++ {
		rows = append(rows, serverstore.WantedRow{Ecosystem: "npm", Name: fmt.Sprintf("blocked-%04d", i), Version: "1.0.0", Symbol: "run", Kind: "EXPANSION", Axis: serverstore.AuthoringAxisSample, Score: 100})
	}
	rows = append(rows, serverstore.WantedRow{Ecosystem: "npm", Name: "claimable-401", Version: "1.0.0", Symbol: "run", Kind: "EXPANSION", Axis: serverstore.AuthoringAxisSample, Score: 1})
	store := newSnapshotStore(rows...)
	base := testNow
	for _, row := range rows[:maxOfferedCandidates] {
		for attempt := 0; attempt < serverstore.AuthoringMaxSessionHandouts; attempt++ {
			_, found, err := store.ClaimAuthoringWork(t.Context(), "window-writer", []serverstore.WantedRow{row}, base.Add(time.Duration(attempt)*serverstore.AuthoringAttemptDebounce), base.Add(24*time.Hour))
			if err != nil || !found {
				t.Fatalf("seed %s attempt %d: found=%v err=%v", row.Name, attempt, found, err)
			}
		}
	}
	// Confirm the seeded prefix is exhausted for this writer before testing
	// whether the HTTP poll can reach the row immediately after it.
	_, found, err := store.ClaimAuthoringSampleWork(t.Context(), "window-writer", rows[:maxOfferedCandidates], base.Add(3*serverstore.AuthoringAttemptDebounce), base.Add(24*time.Hour))
	if err != nil || found {
		t.Fatalf("seeded first window must be barred: found=%v err=%v", found, err)
	}
	srv, _, ck := newTestServer(t, func(d *Deps) { d.Store = store })
	ck.t = base.Add(3 * serverstore.AuthoringAttemptDebounce)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store.Fake, token, "window-writer", ck.t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22","reservation":"SAMPLE"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Status string `json:"status"`
		Work   struct {
			Name string `json:"name"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || result.Status != "ASSIGNED" || result.Work.Name != "claimable-401" {
		t.Fatalf("poll status=%d result=%+v, want claimable-401", resp.StatusCode, result)
	}
}
