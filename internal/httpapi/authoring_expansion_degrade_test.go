package httpapi

// One slow source of work must not take the whole endpoint down with it.
//
// Reported from csx-farm-linux-1 on 2026-09-02: `sample-worker next` returned
// HTTP 503 to every poll from 2026-09-01T22:03Z, and after 02:01Z no work was
// handed out at all. 1115 refusals over seven days, three slots idle, while
// the verification endpoint on the same node answered normally. The farm
// distinguished it from an empty queue itself: csx says NO_WORK when there is
// nothing to do, and this was not that path.
//
// Measured on the production database rather than guessed. The poll runs two
// independent reads, and only one of them is slow:
//
//	TopWanted                        377 ms
//	ListAuthoringExpansionCandidates   83,369 ms   (statement timeout: 10 s)
//
// The expansion query's own timeout cancels it, that cancellation is a query
// timeout, and writeAuthoringWorkBusy turns a query timeout into 503. So a
// single slow query refused every worker in the fleet -- including work that
// the 377 ms read had already found and that nothing was wrong with.
//
// The two are not equals. WANTED is somebody's explicit ask; expansion is the
// network choosing its own next move on top of that. Losing the second should
// narrow what a worker is offered, not stop the farm.
//
// The dominant plan node is a DISTINCT sort of 186,606 rows that yields
// 12,885, spilling on a two-vCPU host. Making it fast is separate work; this
// is about what the endpoint does while one of its sources cannot answer.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestWorkIsStillHandedOutWhenExpansionCandidatesTimeOut(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	sum := sha256.Sum256([]byte(token))
	now := testNow
	if err := store.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: hex.EncodeToString(sum[:]), SessionID: "degrade-worker-1", Label: "worker-01",
		Model: "claude-haiku", Reasoning: "low", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	// Work that the fast read finds, and that nothing is wrong with.
	if err := store.RecordWanted(t.Context(), now.Format("2006-01-02"), "0123456789abcdef", []serverstore.WantedRow{{
		Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post",
	}}); err != nil {
		t.Fatal(err)
	}

	// The slow read, cancelled by its OWN statement timeout. This is the
	// error production returns, not a stand-in for it: PostgreSQL raises
	// 57014 with this exact message when statement_timeout fires, and
	// serverstore.IsQueryTimeout is what classifies it.
	//
	// Not context.DeadlineExceeded. That would be the whole poll running
	// out, which is a different situation and must still refuse.
	store.ExpansionCandidatesErr = &pgconn.PgError{
		Code: "57014", Message: "canceling statement due to statement timeout",
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
		bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Fatalf("the poll was refused with 503 because one of two work sources was slow; "+
			"the other had work. Retry-After=%q", resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}

	var assigned struct {
		Status string `json:"status"`
		Work   struct {
			Package string `json:"package"`
			Symbol  string `json:"symbol"`
		} `json:"work"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Work.Package == "" && assigned.Work.Symbol == "" {
		t.Errorf("no work was handed out (status %q) though WANTED had an uncovered coordinate",
			assigned.Status)
	}
}

// A failure that is NOT the slow query still has to surface. Degrading on
// everything would turn a broken store into a permanently empty farm, which
// looks exactly like having no work and would be found only by noticing that
// nothing is ever produced.
func TestARealExpansionFailureIsNotSwallowed(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	sum := sha256.Sum256([]byte(token))
	now := testNow
	if err := store.IssueAuthoringSessions(t.Context(), []serverstore.AuthoringSessionRow{{
		TokenHash: hex.EncodeToString(sum[:]), SessionID: "degrade-worker-2", Label: "worker-02",
		Model: "claude-haiku", Reasoning: "low", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWanted(t.Context(), now.Format("2006-01-02"), "0123456789abcdef", []serverstore.WantedRow{{
		Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post",
	}}); err != nil {
		t.Fatal(err)
	}

	store.ExpansionCandidatesErr = errBrokenExpansion

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
		bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Error("a store failure that is not a timeout was reported as a successful poll")
	}
}

var errBrokenExpansion = errBroken{}

type errBroken struct{}

func (errBroken) Error() string { return "expansion candidates: relation does not exist" }

// The distinction between "this source timed out" and "the poll ran out of
// time" has to hold by construction, not by which read the clock lands on.
//
// A first version degraded on any busy-classed error, context deadlines
// included. That made TestAuthoringCandidateScanPreservesThePollAbsoluteDeadline
// pass on one machine and fail on another, because whether the deadline
// surfaced depended on whether it hit TopWanted or the expansion query.
func TestOnlyTheExpansionQuerysOwnFailureDegrades(t *testing.T) {
	statementTimeout := &pgconn.PgError{
		Code: "57014", Message: "canceling statement due to statement timeout",
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"the expansion statement hit its own timeout", statementTimeout, true},
		{"the whole poll ran out of time", context.DeadlineExceeded, false},
		{"the caller went away", context.Canceled, false},
		{"the store is broken", errBrokenExpansion, false},
	} {
		if got := expansionUnavailable(tc.err); got != tc.want {
			t.Errorf("%s: expansionUnavailable = %v, want %v", tc.name, got, tc.want)
		}
	}

	// And every one of them still counts as busy for the endpoint's own 503,
	// which is a separate question from whether one source may be dropped.
	for _, err := range []error{statementTimeout, context.DeadlineExceeded, context.Canceled} {
		if !authoringWorkBusyErr(err) {
			t.Errorf("authoringWorkBusyErr(%v) = false", err)
		}
	}
}
