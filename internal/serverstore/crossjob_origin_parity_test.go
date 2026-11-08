package serverstore

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The origin of a sample is the peer behind its first receipt, read the way
// PG reads it: ORDER BY created_at, receipt_id. A cross job is not answered
// by the origin's own receipt.
//
// The fake used to compare every receipt against rs[0] instead of against the
// earliest seen so far, so with receipts stored [t3, t1, t2] the t2 peer
// became the origin. The real origin then closed its own cross job, and the
// genuine second peer was refused as though it had built the sample. Equal
// timestamps had no receipt_id tie-break at all.
func runCompleteJobsForSampleOriginContract(t *testing.T, store Store, backdate func(t *testing.T, receiptID string, at time.Time)) {
	t.Helper()
	ctx := context.Background()
	t1 := time.Unix(1_700_000_100, 0).UTC()
	t2 := t1.Add(time.Minute)
	t3 := t2.Add(time.Minute)
	const (
		peerA = "ed25519:00000000000000aa"
		peerB = "ed25519:00000000000000bb"
		peerC = "ed25519:00000000000000cc"
	)
	type receipt struct {
		key  int // orders receipt_id within a case
		peer string
		at   time.Time
	}
	// Saved in this order, which is not created_at order.
	outOfOrder := []receipt{
		{1, peerA, t3},
		{2, peerB, t1}, // earliest: the origin
		{3, peerC, t2},
	}
	// Two receipts share the earliest instant; the lower receipt_id is the
	// origin even though it was saved second.
	tied := []receipt{
		{2, peerC, t1},
		{1, peerB, t1}, // lower receipt_id: the origin
		{3, peerA, t2},
	}
	cases := []struct {
		name      string
		receipts  []receipt
		completer string
		wantOpen  bool
		wantOrder []int // indexes into receipts
	}{
		{"out of order: the origin does not answer its own cross job", outOfOrder, peerB, true, []int{1, 2, 0}},
		{"out of order: a later peer answers the cross job", outOfOrder, peerC, false, []int{1, 2, 0}},
		{"tie: the lower receipt_id is the origin", tied, peerB, true, []int{1, 0, 2}},
		{"tie: the higher receipt_id answers the cross job", tied, peerC, false, []int{1, 0, 2}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sampleID := "sha256:" + fmt.Sprintf("%064x", 0x100+i)
			// Receipt ids are global, so each case gets its own.
			receiptID := func(key int) string { return "sha256:" + fmt.Sprintf("%060x%04x", 0x100+i, key) }
			if err := store.SaveSample(ctx, SampleRow{SampleID: sampleID, Status: "PUBLISHED",
				ManifestJSON: `{"schemaVersion":1}`}); err != nil {
				t.Fatal(err)
			}
			jobID, err := store.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "cross", Status: "open"})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range tc.receipts {
				id := receiptID(r.key)
				if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: id, SampleID: sampleID,
					PeerID: r.peer, EnvHash: "sha256:e", ReceiptJSON: `{"schemaVersion":1}`,
					ContractResult: "PASS", CreatedAt: r.at}); err != nil {
					t.Fatal(err)
				}
				if backdate != nil {
					backdate(t, id, r.at)
				}
			}

			rows, err := store.ReceiptsForSample(ctx, sampleID)
			if err != nil {
				t.Fatal(err)
			}
			var got, want []string
			for _, r := range rows {
				got = append(got, r.ReceiptID)
			}
			for _, k := range tc.wantOrder {
				want = append(want, receiptID(tc.receipts[k].key))
			}
			if !slices.Equal(got, want) {
				t.Fatalf("ReceiptsForSample = %v, want created_at, receipt_id order %v", got, want)
			}

			if err := store.CompleteJobsForSample(ctx, sampleID, tc.completer); err != nil {
				t.Fatal(err)
			}
			jobs, err := store.JobsForSample(ctx, sampleID)
			if err != nil {
				t.Fatal(err)
			}
			var status string
			for _, j := range jobs {
				if j.ID == jobID {
					status = j.Status
				}
			}
			if open := status == "open"; open != tc.wantOpen {
				t.Fatalf("cross job status after %s's receipt = %q, want open=%v", tc.completer, status, tc.wantOpen)
			}
		})
	}
}

func TestCompleteJobsForSampleOriginParity(t *testing.T) {
	runCompleteJobsForSampleOriginContract(t, NewFake(), nil)
}

func TestIntegrationPGCompleteJobsForSampleOriginParity(t *testing.T) {
	pg := openTestPG(t)
	runCompleteJobsForSampleOriginContract(t, pg, func(t *testing.T, receiptID string, at time.Time) {
		t.Helper()
		// PG stamps created_at itself; set the instant the case is about.
		if err := pg.withConn(context.Background(), func(c *pgx.Conn) error {
			_, err := c.Exec(context.Background(),
				`UPDATE receipts SET created_at=$1 WHERE receipt_id=$2`, at, receiptID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}
