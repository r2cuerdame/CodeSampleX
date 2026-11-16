package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type stalledRegistry struct{}

func (stalledRegistry) Check(ctx context.Context, _ domain.PURL) string {
	<-ctx.Done()
	return scanner.PublicnessUnknown
}

func (stalledRegistry) MavenHasJar(ctx context.Context, _ domain.PURL) (bool, bool) {
	<-ctx.Done()
	return false, false
}

type claimReserveStore struct {
	*serverstore.Fake
	expansion   []serverstore.WantedRow
	windowDelay time.Duration
	windows     atomic.Int32
}

func (s *claimReserveStore) ListAuthoringExpansionCandidates(ctx context.Context, limit int) ([]serverstore.WantedRow, error) {
	if s.expansion != nil {
		return s.expansion, nil
	}
	return s.Fake.ListAuthoringExpansionCandidates(ctx, limit)
}

func (s *claimReserveStore) FilterIncompleteAuthoringCandidates(ctx context.Context, rows []serverstore.WantedRow) ([]serverstore.WantedRow, error) {
	if s.expansion != nil {
		return rows, nil
	}
	return s.Fake.FilterIncompleteAuthoringCandidates(ctx, rows)
}

func (s *claimReserveStore) ClaimAuthoringWork(ctx context.Context, sessionID string, rows []serverstore.WantedRow, now, leaseExpiresAt time.Time) (serverstore.AuthoringWorkRow, bool, error) {
	if err := ctx.Err(); err != nil {
		return serverstore.AuthoringWorkRow{}, false, err
	}
	if s.windowDelay > 0 {
		s.windows.Add(1)
		select {
		case <-ctx.Done():
			return serverstore.AuthoringWorkRow{}, false, ctx.Err()
		case <-time.After(s.windowDelay):
			return serverstore.AuthoringWorkRow{}, false, nil
		}
	}
	return s.Fake.ClaimAuthoringWork(ctx, sessionID, rows, now, leaseExpiresAt)
}

func postClaimReservePoll(t *testing.T, store *claimReserveStore, checker PublicnessChecker, timeout time.Duration, token string) (*http.Response, map[string]any) {
	t.Helper()
	srv := httptest.NewServer(NewMux(Deps{Store: store, Checker: checker, Now: func() time.Time { return testNow }, authoringWorkTimeout: timeout}))
	t.Cleanup(srv.Close)
	envelope := `{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next", strings.NewReader(envelope))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestAuthoringWorkStalledRegistryKeepsClaimBudget(t *testing.T) {
	const token = "csx_author_v1_YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI"
	store := &claimReserveStore{Fake: serverstore.NewFake()}
	authoringSession(t, store.Fake, token, "writer-claim-reserve", testNow)
	if err := store.RecordWanted(t.Context(), testNow.Format("2006-01-02"), "0123456789abcdef", []serverstore.WantedRow{
		{Ecosystem: "maven", Name: "com.example/widget", Version: "1.0.0", Symbol: "com.example.Widget"},
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post"},
	}); err != nil {
		t.Fatal(err)
	}
	store.expansion = []serverstore.WantedRow{{
		Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", Symbol: "leftPad",
		Kind: "DEPENDENCY", Axis: serverstore.AuthoringAxisSample,
	}}
	resp, body := postClaimReservePoll(t, store, stalledRegistry{}, 900*time.Millisecond, token)
	if resp.StatusCode != http.StatusOK || body["status"] != "ASSIGNED" {
		t.Fatalf("status=%d reason=%q body=%v, want an assignment while the registry stalls",
			resp.StatusCode, resp.Header.Get("X-CSX-503-Reason"), body)
	}
}

func TestAuthoringWorkClaimWindowsStopInsideThePoll(t *testing.T) {
	const token = "csx_author_v1_Y2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2M"
	store := &claimReserveStore{Fake: serverstore.NewFake(), windowDelay: 600 * time.Millisecond}
	authoringSession(t, store.Fake, token, "writer-claim-windows", testNow)
	for i := range 3*maxOfferedCandidates - 50 {
		store.expansion = append(store.expansion, serverstore.WantedRow{
			Ecosystem: "npm", Name: fmt.Sprintf("pkg-%04d", i), Version: "1.0.0", Symbol: "main",
			Kind: "EXPANSION", Axis: serverstore.AuthoringAxisSample,
		})
	}
	resp, body := postClaimReservePoll(t, store, nil, 1500*time.Millisecond, token)
	if resp.StatusCode != http.StatusOK || body["status"] != "NO_WORK" {
		t.Fatalf("status=%d reason=%q body=%v, want NO_WORK once the remaining windows cannot finish",
			resp.StatusCode, resp.Header.Get("X-CSX-503-Reason"), body)
	}
	if got := store.windows.Load(); got != 2 {
		t.Fatalf("claim windows = %d, want 2", got)
	}
}
