package serverstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIntegrationAdminRequestCoverageUsesRecentDemandAndSnapshotBoundaries(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO packages(purl,ecosystem,name,version,major,publicness)
			VALUES
			 ('pkg:npm/axios@1.12.0','npm','axios','1.12.0','1','PUBLIC'),
			 ('pkg:npm/axios@1.11.0','npm','axios','1.11.0','1','PUBLIC'),
			 ('pkg:pypi/requests@2.32.3','pypi','requests','2.32.3','2','PUBLIC');
			INSERT INTO compatibility_snapshots(purl,symbol,snapshot)
			VALUES
			 ('pkg:npm/axios@1.12.0','axios.get',
			  '{"rows":[{"envBucket":{"os":"darwin"},"byStage":{"CONTRACT":{"pass":2,"fail":0}}}]}'::jsonb),
			 ('pkg:npm/axios@1.12.0','axios.put',
			  '{"rows":[{"envBucket":{"os":"linux"},"byStage":{"CONTRACT":{"pass":0,"fail":4}}},{"envBucket":{"os":"windows"},"byStage":{"CONTRACT":{"pass":2,"fail":0}}}],"failures":[{"stage":"CONTRACT","fingerprint":"sha256:android","count":3,"reporters":1,"envSummary":{"os":"android"}},{"stage":"CONTRACT","fingerprint":"sha256:windows","count":9,"reporters":2,"envSummary":{"os":"windows"}}]}'::jsonb),
			 ('pkg:npm/axios@1.11.0','',
			  '{"rows":[{"envBucket":{"os":"linux"},"byStage":{"CONTRACT":{"pass":1,"fail":0}}}]}'::jsonb),
			 ('pkg:npm/axios@1.11.0','axios.put',
			  '{"rows":[],"failures":[{"stage":"CONTRACT","fingerprint":"sha256:old-version","count":999,"reporters":999,"envSummary":{"os":"android"}}]}'::jsonb),
			 ('pkg:pypi/requests@2.32.3','',
			  '{"rows":[{"envBucket":{"os":"linux"},"byStage":{"PROJECT_TEST":{"pass":4}}}]}'::jsonb);
			INSERT INTO wanted_dedup(ecosystem,name,version,symbol,target_os,epoch,anon_id)
			VALUES
			 ('npm','axios','1.12.0','axios.post','','2026-08-24','a'),
			 ('npm','axios','1.12.0','axios.post','','2026-08-23','b'),
			 ('npm','axios','1.12.0','axios.get','macos','2026-08-24','c'),
			 ('npm','axios','1.12.0','axios.put','android','2026-08-24','f'),
			 ('npm','axios','1.11.0','','','2026-08-22','d'),
			 ('pypi','requests','2.32.3','Session.get','','2026-08-24','e'),
			 ('cargo','old','1.0.0','','','2026-08-17','old')`)
		return err
	}); err != nil {
		t.Fatalf("seed request coverage: %v", err)
	}

	var got AdminRequestCoverage
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		var err error
		got, err = adminRequestCoverage(ctx, c, now)
		return err
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Fatalf("adminRequestCoverage: %v (position %d)", err, pgErr.Position)
		}
		t.Fatalf("adminRequestCoverage: %v", err)
	}
	if got.WindowStart != "2026-08-18" || got.WindowEnd != "2026-08-24" || got.TotalImpact != 6 || got.PackageCount != 2 {
		t.Fatalf("coverage window/totals = %+v", got)
	}
	if len(got.Nodes) != 5 {
		t.Fatalf("coverage nodes = %+v", got.Nodes)
	}
	bySymbol := map[string]AdminCoverageNode{}
	for _, node := range got.Nodes {
		bySymbol[node.Symbol+"|"+node.TargetOS] = node
		if node.LastDay == "2026-08-17" {
			t.Fatalf("out-of-window demand leaked into result: %+v", node)
		}
	}
	if node := bySymbol["axios.post|"]; node.Impact != 2 || node.State != AdminCoveragePartial || node.Boundary != "symbol_gap" {
		t.Fatalf("symbol gap = %+v", node)
	}
	if node := bySymbol["axios.get|macos"]; node.State != AdminCoverageHit || node.Boundary != "exact_pass" {
		t.Fatalf("darwin/macos alias = %+v", node)
	}
	if node := bySymbol["axios.put|android"]; node.State != AdminCoveragePartial || node.Boundary != "environment_gap" || node.NearestEnvironment != "windows" || node.FailureFingerprint != "sha256:android" {
		t.Fatalf("pass-only nearest environment and scoped failure = %+v", node)
	}
	if node := bySymbol["|"]; node.State != AdminCoverageHit || node.Boundary != "exact_pass" {
		t.Fatalf("exact package hit = %+v", node)
	}
	if node := bySymbol["Session.get|"]; node.State != AdminCoveragePartial || node.Boundary != "symbol_gap" {
		t.Fatalf("requests symbol gap = %+v", node)
	}
	for _, node := range got.Nodes {
		if !node.InMap {
			t.Fatalf("small fixture node unexpectedly excluded from map: %+v", node)
		}
		if (node.State != AdminCoverageHit) != node.InTopMissing {
			t.Fatalf("top missing flag disagrees with exact-pass state: %+v", node)
		}
	}
}

func TestIntegrationAdminRequestCoverageRanksMissingOutsideMapPackages(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `
			INSERT INTO packages(purl,ecosystem,name,version,major,publicness)
			SELECT format('pkg:npm/hitpkg%s@1.0.0', n),'npm',format('hitpkg%s', n),'1.0.0','1','PUBLIC'
			  FROM generate_series(1,10) n;
			INSERT INTO compatibility_snapshots(purl,symbol,snapshot)
			SELECT format('pkg:npm/hitpkg%s@1.0.0', n),'covered',
			       '{"rows":[{"envBucket":{"os":"linux"},"byStage":{"CONTRACT":{"pass":1,"fail":0}}}]}'::jsonb
			  FROM generate_series(1,10) n;
			INSERT INTO wanted_dedup(ecosystem,name,version,symbol,target_os,epoch,anon_id)
			SELECT 'npm',format('hitpkg%s', n),'1.0.0','covered','','2026-08-24',format('covered-%s-%s',n,reporter)
			  FROM generate_series(1,10) n CROSS JOIN generate_series(1,100) reporter;
			INSERT INTO wanted_dedup(ecosystem,name,version,symbol,target_os,epoch,anon_id)
			SELECT 'npm',format('hitpkg%s', n),'1.0.0','small-gap','','2026-08-24',format('gap-%s',n)
			  FROM generate_series(1,10) n;
			INSERT INTO wanted_dedup(ecosystem,name,version,symbol,target_os,epoch,anon_id)
			SELECT 'npm','outside-map','1.0.0','large-gap','','2026-08-24',format('outside-%s',reporter)
			  FROM generate_series(1,90) reporter`)
		return err
	}); err != nil {
		t.Fatalf("seed ranked coverage: %v", err)
	}

	var got AdminRequestCoverage
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		var err error
		got, err = adminRequestCoverage(ctx, c, now)
		return err
	}); err != nil {
		t.Fatalf("adminRequestCoverage: %v", err)
	}
	var outside *AdminCoverageNode
	for i := range got.Nodes {
		if got.Nodes[i].Name == "outside-map" {
			outside = &got.Nodes[i]
			break
		}
	}
	if outside == nil || outside.Impact != 90 || outside.InMap || !outside.InTopMissing || outside.State != AdminCoverageMiss {
		t.Fatalf("high-impact gap outside top packages = %+v", outside)
	}
	if got.PackageCount != 10 || got.TotalImpact != 1100 {
		t.Fatalf("bounded package map totals = %+v", got)
	}
}
