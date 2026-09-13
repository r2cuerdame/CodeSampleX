package serverstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// One snapshot supplies channel-wide all-time counts and the filtered page.
// No report JSON or reporter bucket is selected for the queue.
const reportQueueSQL = `WITH reports AS (
 SELECT id, 'product' AS channel, issue_kind AS kind, surface || ' · ' || component AS target,
 status, verdict, replay_reason AS reason, canonical_ref AS "canonicalRef", occurrences,
 first_seen AS "firstSeen", last_seen AS "lastSeen"
 FROM csx_issue_reports WHERE $1 IN ('all','product')
 UNION ALL
 SELECT id, 'anomaly', anomaly_type, purl || ' · ' || symbol,
 status, verdict, unsupported_reason, '', reports, first_seen, last_seen
 FROM anomaly_reports WHERE $1 IN ('all','anomaly')
), classified AS (
 SELECT *, verdict='' AND status IN ('unsupported','no-replay-lane') AS blocked,
 channel='product' AND verdict='confirmed-csx-defect' AND "canonicalRef"='' AS unlinked
 FROM reports
), filtered AS (
 SELECT * FROM classified WHERE
 ($2='all' OR ($2='open' AND verdict='') OR ($2='resolved' AND verdict<>'') OR ($2='blocked' AND blocked) OR ($2='unlinked' AND unlinked))
 AND strpos(lower(id::text || ' ' || kind || ' ' || target || ' ' || status || ' ' || verdict || ' ' || reason || ' ' || "canonicalRef"),$3)>0
), page AS (
 SELECT id,channel,kind,target,status,verdict,reason,"canonicalRef",occurrences,"firstSeen","lastSeen"
 FROM filtered ORDER BY "lastSeen" DESC,id DESC,channel ASC LIMIT $4 OFFSET $5
)
SELECT json_build_object('rows',COALESCE((SELECT json_agg(page) FROM page),'[]'::json),
 'total',(SELECT count(*) FROM filtered),
 'counts',(SELECT json_build_object('total',count(*),'open',count(*) FILTER (WHERE verdict=''),
 'resolved',count(*) FILTER (WHERE verdict<>''),'blocked',count(*) FILTER (WHERE blocked),
 'unlinked',count(*) FILTER (WHERE unlinked)) FROM classified))`

func (p *PG) ReportQueue(ctx context.Context, filter ReportFilter) (ReportPage, error) {
	filter = filter.normalized()
	out := ReportPage{}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		var raw []byte
		if err := c.QueryRow(ctx, reportQueueSQL, filter.Channel, filter.State, filter.Query, filter.Limit, filter.Offset).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &out)
	})
	return out, err
}

func (p *PG) ReviewProductReport(ctx context.Context, id int64, verdict, note, ref string, at time.Time) (bool, error) {
	updated := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tag, err := c.Exec(ctx, `UPDATE csx_issue_reports SET verdict=$2,review_note=$3,canonical_ref=$4,verdict_at=$5,status='resolved' WHERE id=$1 AND verdict=''`, id, verdict, note, ref, at)
		updated = tag.RowsAffected() > 0
		return err
	})
	return updated, err
}

func (p *PG) ProductReviewNote(ctx context.Context, id int64) (string, error) {
	note := ""
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT review_note FROM csx_issue_reports WHERE id=$1`, id).Scan(&note)
	})
	return note, err
}
