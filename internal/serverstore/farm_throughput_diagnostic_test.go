package serverstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const throughputTestPeer = "ed25519:dddddddddddddddd"

type throughputGENCounts struct {
	Slot                            int `json:"slot"`
	CurrentSlotAttributedDrafts     int `json:"currentSlotAttributedDrafts"`
	CreatedAndUpdatedTimestampEqual int `json:"createdAndUpdatedTimestampEqual"`
	PostcreationTimestampChanged    int `json:"postcreationTimestampChanged"`
}

type throughputCounts struct {
	WindowStart string `json:"windowStart"`
	WindowEnd   string `json:"windowEnd"`
	Receipts    struct {
		Accepted int `json:"accepted"`
		Pass     int `json:"pass"`
	} `json:"receipts"`
	SampleFirstPass struct {
		UnambiguousNodeSamples            int `json:"unambiguousNodeSamples"`
		SharedFirstTimestampSamples       int `json:"sharedFirstTimestampSamples"`
		UnknownHistoricalTimestampSamples int `json:"unknownHistoricalTimestampSamples"`
	} `json:"sampleFirstPass"`
	GEN struct {
		throughputGENCounts
		Slots []throughputGENCounts `json:"slots"`
	} `json:"gen"`
}

func throughputDiagnosticSQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../deploy/lightsail/farm-throughput.sql")
	if err != nil {
		t.Fatal(err)
	}
	query := strings.ReplaceAll(string(raw), "__NODE_PEER__", throughputTestPeer)
	for slot := 1; slot <= 3; slot++ {
		label := fmt.Sprintf("private-node-slot%d", slot)
		query = strings.ReplaceAll(query, fmt.Sprintf("__SLOT%d_HASH__", slot), fmt.Sprintf("%x", sha256.Sum256([]byte(label))))
	}
	// Freeze only the server clock for exact inclusive/exclusive boundary
	// fixtures. All query predicates and projections are the shipped SQL file.
	return strings.ReplaceAll(query, "statement_timestamp()", "'2026-09-11T20:00:00Z'::timestamptz")
}

func readThroughputDiagnostic(t *testing.T, pg *PG, query string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var raw []byte
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='1s'; SET LOCAL lock_timeout='500ms'; SET LOCAL jit=off`); err != nil {
			return err
		}
		return tx.QueryRow(ctx, query).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestIntegrationFarmThroughputScalarSemantics(t *testing.T) {
	pg := openTestPG(t)
	query := throughputDiagnosticSQL(t)
	read := func() throughputCounts {
		t.Helper()
		raw := readThroughputDiagnostic(t, pg, query)
		if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), throughputTestPeer) {
			t.Fatalf("scalar result leaked a private identifier: %s", raw)
		}
		var result throughputCounts
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if result.WindowStart != "2026-09-11T19:00:00.000000Z" || result.WindowEnd != "2026-09-11T20:00:00.000000Z" {
			t.Fatalf("wrong frozen server window: %+v", result)
		}
		return result
	}
	if empty := read(); empty.Receipts.Accepted != 0 || empty.GEN.CurrentSlotAttributedDrafts != 0 || len(empty.GEN.Slots) != 3 {
		t.Fatalf("empty corpus counters: %+v", empty)
	}
	ctx := t.Context()
	for _, sample := range []string{"new", "prior-other", "prior-node", "old-fail", "tie", "unknown", "start", "end", "fail", "other"} {
		// All samples deliberately share one PURL. First-PASS must count
		// samples, not the separately owned network PURL firstProven metric.
		if err := pg.SaveSample(ctx, SampleRow{SampleID: "private-" + sample,
			ManifestJSON: `{"packages":["pkg:npm/shared@1.0.0"]}`, Quarantined: sample == "new"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		for _, receipt := range []struct{ id, sample, peer, result, at string }{
			{"new-1", "new", throughputTestPeer, "PASS", "2026-09-11T19:10:00Z"},
			{"new-1", "new", throughputTestPeer, "PASS", "2026-09-11T19:10:00Z"}, // stable-ID retry
			{"new-2", "new", throughputTestPeer, "PASS", "2026-09-11T19:20:00Z"},
			{"prior-other-old", "prior-other", "other", "PASS", "2026-09-11T18:00:00Z"},
			{"prior-other-now", "prior-other", throughputTestPeer, "PASS", "2026-09-11T19:05:00Z"},
			{"prior-node-old", "prior-node", throughputTestPeer, "PASS", "2026-09-11T18:02:00Z"},
			{"prior-node-now", "prior-node", throughputTestPeer, "PASS", "2026-09-11T19:06:00Z"},
			{"old-fail", "old-fail", "other", "FAIL", "2026-09-11T18:03:00Z"},
			{"old-fail-pass", "old-fail", throughputTestPeer, "PASS", "2026-09-11T19:07:00Z"},
			{"tie-other", "tie", "other", "PASS", "2026-09-11T19:08:00Z"},
			{"tie-node", "tie", throughputTestPeer, "PASS", "2026-09-11T19:08:00Z"},
			{"unknown-old", "unknown", "other", "PASS", ""},
			{"unknown-node", "unknown", throughputTestPeer, "PASS", "2026-09-11T19:09:00Z"},
			{"start", "start", throughputTestPeer, "PASS", "2026-09-11T19:00:00Z"},
			{"end", "end", throughputTestPeer, "PASS", "2026-09-11T20:00:00Z"},
			{"fail", "fail", throughputTestPeer, "FAIL", "2026-09-11T19:45:00Z"},
			{"other", "other", "other", "PASS", "2026-09-11T19:50:00Z"},
		} {
			if _, err := c.Exec(ctx, `INSERT INTO receipts(receipt_id,sample_id,peer_id,env_hash,receipt,contract_result,created_at)
				VALUES($1,$2,$3,'private-env','{"private-secret":"not-selected"}',$4,NULLIF($5,'')::timestamptz)
				ON CONFLICT(receipt_id) DO NOTHING`, "private-"+receipt.id, "private-"+receipt.sample, receipt.peer, receipt.result, receipt.at); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stamp := func(value string) time.Time {
		v, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	saveDraft := func(id, label, created, updated string) {
		t.Helper()
		if err := pg.SaveAuthoringDraft(ctx, AuthoringDraftRow{SampleID: "private-" + id,
			SessionID: "private-expired-session", WorkerLabel: label, ManifestJSON: "{}", LocalStatus: "LOCAL_PASS",
			CreatedAt: stamp(created), UpdatedAt: stamp(updated)}); err != nil {
			t.Fatal(err)
		}
	}
	saveDraft("draft-1", "private-node-slot1", "2026-09-11T19:00:00Z", "2026-09-11T19:00:00Z")
	saveDraft("draft-2", "private-node-slot2", "2026-09-11T19:15:00Z", "2026-09-11T21:00:00Z")
	saveDraft("draft-3", "private-node-slot3", "2026-09-11T19:20:00Z", "2026-09-11T19:20:00Z")
	saveDraft("draft-old", "private-node-slot1", "2026-09-11T18:59:59Z", "2026-09-11T19:30:00Z")
	saveDraft("draft-end", "private-node-slot1", "2026-09-11T20:00:00Z", "2026-09-11T20:00:00Z")
	saveDraft("draft-other", "private-other-slot1", "2026-09-11T19:10:00Z", "2026-09-11T19:10:00Z")
	if before := read(); before.GEN.CurrentSlotAttributedDrafts != 3 || before.GEN.CreatedAndUpdatedTimestampEqual != 2 {
		t.Fatalf("initial current-label attribution: %+v", before.GEN)
	}
	// Reassignment preserves first creation time while changing current owner.
	// There are no live sessions in this fixture, so renewal cannot hide rows.
	saveDraft("draft-3", "private-node-slot1", "2026-09-11T19:40:00Z", "2026-09-11T19:40:00Z")
	got := read()
	if got.Receipts.Accepted != 9 || got.Receipts.Pass != 8 || got.SampleFirstPass.UnambiguousNodeSamples != 3 ||
		got.SampleFirstPass.SharedFirstTimestampSamples != 1 || got.SampleFirstPass.UnknownHistoricalTimestampSamples != 1 {
		t.Fatalf("receipt/first-PASS semantics: %+v", got)
	}
	if got.GEN.CurrentSlotAttributedDrafts != 3 || got.GEN.CreatedAndUpdatedTimestampEqual != 1 || got.GEN.PostcreationTimestampChanged != 2 ||
		got.GEN.Slots[0].CurrentSlotAttributedDrafts != 2 || got.GEN.Slots[1].CurrentSlotAttributedDrafts != 1 || got.GEN.Slots[2].CurrentSlotAttributedDrafts != 0 {
		t.Fatalf("GEN mutable-current-owner semantics: %+v", got.GEN)
	}
}

func TestIntegrationFarmThroughputReadsStayWindowAndSampleBounded(t *testing.T) {
	pg := openTestPG(t)
	ctx := t.Context()
	query := throughputDiagnosticSQL(t)
	for _, scale := range []int{1000, 10000} {
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, `INSERT INTO samples(sample_id,manifest,size_bytes)
				SELECT 'private-noise-'||g,'{}',0 FROM generate_series(1,$1::int) g ON CONFLICT DO NOTHING;
				INSERT INTO receipts(receipt_id,sample_id,peer_id,env_hash,receipt,contract_result,created_at)
				SELECT 'private-noise-r-'||g,'private-noise-'||g,'other','env','{}','PASS','2026-09-09' FROM generate_series(1,$1::int) g ON CONFLICT DO NOTHING;
				INSERT INTO authoring_drafts(sample_id,session_id,worker_label,manifest,local_status,created_at,updated_at)
				SELECT 'private-noise-d-'||g,'expired','private-node-slot1','{}','LOCAL_PASS','2026-09-09','2026-09-09'
				FROM generate_series(1,$1::int) g ON CONFLICT DO NOTHING;
				INSERT INTO receipts(receipt_id,sample_id,peer_id,env_hash,receipt,contract_result,created_at)
				VALUES('private-candidate','private-noise-1','ed25519:dddddddddddddddd','env','{}','PASS','2026-09-11T19:15:00Z') ON CONFLICT DO NOTHING;
				ANALYZE samples, receipts, authoring_drafts`, pgx.QueryExecModeSimpleProtocol, scale)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		raw := readThroughputDiagnostic(t, pg, "EXPLAIN (ANALYZE, FORMAT JSON) "+query)
		type node struct {
			Relation string  `json:"Relation Name"`
			Index    string  `json:"Index Name"`
			Rows     float64 `json:"Actual Rows"`
			Loops    float64 `json:"Actual Loops"`
			Removed  float64 `json:"Rows Removed by Filter"`
			Plans    []node  `json:"Plans"`
		}
		var plans []struct{ Plan node }
		if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
			t.Fatalf("decode plan: %v", err)
		}
		indexes := map[string]bool{}
		var rows float64
		var walk func(node)
		walk = func(n node) {
			indexes[n.Index] = true
			if n.Relation == "receipts" || n.Relation == "authoring_drafts" {
				rows += (n.Rows + n.Removed) * n.Loops
			}
			for _, child := range n.Plans {
				walk(child)
			}
		}
		walk(plans[0].Plan)
		for _, index := range []string{"receipts_created_result_idx", "receipts_sample_idx", "authoring_drafts_updated_idx"} {
			if !indexes[index] {
				t.Fatalf("scale=%d omitted bounded index %s", scale, index)
			}
		}
		if rows > 8 {
			t.Fatalf("scale=%d touched %.0f unrelated receipt/draft rows", scale, rows)
		}
		t.Logf("irrelevant=%d receipt/draft actual rows=%.0f", scale, rows)
	}
}
