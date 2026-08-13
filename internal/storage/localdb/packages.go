package localdb

import (
	"context"
	"database/sql"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// PackageRow is one locally known dependency with its cached publicness
// verdict. Local diagnostic data only — never uploaded.
type PackageRow struct {
	PURL       domain.PURL
	Public     bool
	Publicness string // PUBLIC | PRIVATE | UNKNOWN
	FirstSeen  time.Time
	LastSeen   time.Time
	CheckedAt  time.Time // when the registry publicness check last ran; zero = never
}

// UpsertPackage records a scan sighting of purl with the scanner's
// publicness. An UNKNOWN sighting never downgrades an existing verdict
// (the registry check owns upgrades to PUBLIC and sets checked_at;
// scanners only know PRIVATE for file:/link:/git/workspace deps).
func (d *DB) UpsertPackage(ctx context.Context, purl domain.PURL, publicness string) error {
	now := nowText()
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO packages(purl, ecosystem, name, version, public, publicness, first_seen, last_seen)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(purl) DO UPDATE SET
		  last_seen = excluded.last_seen,
		  publicness = CASE WHEN excluded.publicness = 'UNKNOWN' THEN packages.publicness ELSE excluded.publicness END,
		  public     = CASE WHEN excluded.publicness = 'UNKNOWN' THEN packages.public     ELSE excluded.public     END`,
		purl.String(), purl.Ecosystem, purl.Name, purl.Version,
		boolInt(publicness == "PUBLIC"), publicness, now, now)
	return err
}

// SetPublicness records the outcome of a registry publicness check,
// stamping checked_at so callers can expire the cache.
func (d *DB) SetPublicness(ctx context.Context, purl domain.PURL, status string) error {
	now := nowText()
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO packages(purl, ecosystem, name, version, public, publicness, first_seen, last_seen, checked_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(purl) DO UPDATE SET
		  public = excluded.public, publicness = excluded.publicness,
		  last_seen = excluded.last_seen, checked_at = excluded.checked_at`,
		purl.String(), purl.Ecosystem, purl.Name, purl.Version,
		boolInt(status == "PUBLIC"), status, now, now, now)
	return err
}

// GetPublicness returns the cached verdict for purl. ok is false when the
// package was never seen (callers treat that as UNKNOWN ⇒ excluded, the
// safe default, so query errors also report ok=false).
func (d *DB) GetPublicness(ctx context.Context, purl domain.PURL) (status string, checkedAt time.Time, ok bool) {
	var checked sql.NullString
	err := d.sql.QueryRowContext(ctx,
		`SELECT publicness, checked_at FROM packages WHERE purl = ?`,
		purl.String()).Scan(&status, &checked)
	if err != nil {
		return "", time.Time{}, false
	}
	return status, parseTimeText(checked), true
}

// ListPackages returns every known package ordered by purl.
func (d *DB) ListPackages(ctx context.Context) ([]PackageRow, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT ecosystem, name, version, public, publicness, first_seen, last_seen, checked_at
		FROM packages ORDER BY purl`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageRow
	for rows.Next() {
		var r PackageRow
		var public int
		var first, last, checked sql.NullString
		if err := rows.Scan(&r.PURL.Ecosystem, &r.PURL.Name, &r.PURL.Version,
			&public, &r.Publicness, &first, &last, &checked); err != nil {
			return nil, err
		}
		r.Public = public != 0
		r.FirstSeen = parseTimeText(first)
		r.LastSeen = parseTimeText(last)
		r.CheckedAt = parseTimeText(checked)
		out = append(out, r)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
