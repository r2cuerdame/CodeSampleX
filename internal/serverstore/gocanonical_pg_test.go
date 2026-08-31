package serverstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The fold in 0030 merges a bare Go version onto its canonical twin, and
// carries the facts that only the bare row held.
//
// ParsePURL repairs the spelling on the way in now, which is what makes this
// fold necessary rather than tidy: a stored bare row is a coordinate no parsed
// purl can reach any more, so leaving it would strand its evidence instead of
// merging it.
//
// Both directions are tested, because a migration runs on databases nobody
// measured. Production on 2026-08-31 happened to have 6 package rows with a
// twin and 2 without, and 16 wanted rows with no twin at all — a fixture built
// only from what was there would have left the merge path untested on the
// table where the undercount actually showed.
func TestTheGoVersionFoldMergesAndCarriesTheFacts(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	early := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)

	exec := func(sql string, args ...any) {
		t.Helper()
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, sql, args...)
			return err
		}); err != nil {
			t.Fatalf("%v\nSQL: %s", err, strings.TrimSpace(sql))
		}
	}
	row := func(sql string, dest ...any) {
		t.Helper()
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			return c.QueryRow(ctx, sql).Scan(dest...)
		}); err != nil {
			t.Fatalf("%v\nSQL: %s", err, strings.TrimSpace(sql))
		}
	}

	// A canonical row and its bare twin, where only the bare one knows the
	// earliest sighting and only it has a decided publicness.
	exec(`INSERT INTO packages(purl,ecosystem,name,version,major,publicness,checked_at,first_seen,last_seen)
		VALUES('pkg:golang/example.com/fold@v1.0.0','golang','example.com/fold','v1.0.0','v1','UNKNOWN',$1,$1,$1)`, late)
	exec(`INSERT INTO packages(purl,ecosystem,name,version,major,publicness,checked_at,first_seen,last_seen)
		VALUES('pkg:golang/example.com/fold@1.0.0','golang','example.com/fold','1.0.0','v1','PUBLIC',$1,$1,$2)`, early, late)
	// And a bare row with no twin, which must simply be renamed.
	exec(`INSERT INTO packages(purl,ecosystem,name,version,major,publicness,checked_at,first_seen,last_seen)
		VALUES('pkg:golang/example.com/fold@2.0.0','golang','example.com/fold','2.0.0','v2','PUBLIC',$1,$1,$1)`, late)

	// wanted: the undercount this issue was found through. Two rows for one
	// release, asks that do not add up.
	exec(`INSERT INTO wanted(ecosystem,name,symbol,asks,first_seen,last_seen,version,target_os)
		VALUES('golang','example.com/fold','Fold.New',3,$1,$1,'v1.0.0','')`, late)
	exec(`INSERT INTO wanted(ecosystem,name,symbol,asks,first_seen,last_seen,version,target_os)
		VALUES('golang','example.com/fold','Fold.New',4,$1,$2,'1.0.0','')`, early, late)

	fold := func() {
		t.Helper()
		migs, err := LoadMigrations()
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range migs {
			if m.Version != "0030_canonical_go_versions.sql" {
				continue
			}
			for _, stmt := range m.Statements {
				if strings.TrimSpace(stmt) != "" {
					exec(stmt)
				}
			}
			return
		}
		t.Fatal("0030_canonical_go_versions.sql not loaded")
	}
	fold()

	var bare int
	row(`SELECT count(*) FROM packages WHERE name='example.com/fold' AND version !~ '^v'`, &bare)
	if bare != 0 {
		t.Errorf("%d bare package rows survived", bare)
	}
	var publicness, purl string
	var firstSeen time.Time
	row(`SELECT publicness, purl, first_seen FROM packages
		WHERE name='example.com/fold' AND version='v1.0.0'`, &publicness, &purl, &firstSeen)
	if publicness != "PUBLIC" {
		t.Errorf("publicness = %q: a decided answer lost to UNKNOWN in the merge", publicness)
	}
	if !firstSeen.UTC().Equal(early) {
		t.Errorf("first_seen = %s, want %s: the earliest sighting was only on the bare row",
			firstSeen.UTC(), early)
	}
	if purl != "pkg:golang/example.com/fold@v1.0.0" {
		t.Errorf("purl = %q", purl)
	}
	row(`SELECT purl FROM packages WHERE name='example.com/fold' AND version='v2.0.0'`, &purl)
	if purl != "pkg:golang/example.com/fold@v2.0.0" {
		t.Errorf("a bare row with no twin became %q", purl)
	}

	// wanted: the asks add up now, which is the point.
	var rows, asks int
	row(`SELECT count(*), coalesce(sum(asks),0)::int FROM wanted
		WHERE name='example.com/fold' AND symbol='Fold.New'`, &rows, &asks)
	if rows != 1 {
		t.Errorf("%d wanted rows for one release, want 1", rows)
	}
	if asks != 7 {
		t.Errorf("asks = %d, want 7: a merge that drops asks ranks a real demand lower than it is", asks)
	}

	// Running it again changes nothing. A fold that ran twice would double the
	// asks it had just merged, and migrations are re-applied by hand often
	// enough that this is worth pinning.
	fold()
	row(`SELECT count(*), coalesce(sum(asks),0)::int FROM wanted
		WHERE name='example.com/fold' AND symbol='Fold.New'`, &rows, &asks)
	if rows != 1 || asks != 7 {
		t.Errorf("re-running the fold gave %d rows / %d asks, want 1 / 7", rows, asks)
	}
}
