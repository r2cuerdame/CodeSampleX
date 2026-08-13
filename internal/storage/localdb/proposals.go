package localdb

import (
	"context"
	"strings"
	"time"
)

// ProposalRow is one sample workspace an agent prepared, waiting for the
// user to review and publish it.
type ProposalRow struct {
	Workdir   string
	Goal      string
	Packages  []string
	CreatedAt time.Time
	State     string // pending | published | dropped
}

// SaveProposal records a prepared sample workspace. Re-proposing into the
// same workspace updates it rather than duplicating.
func (d *DB) SaveProposal(ctx context.Context, r ProposalRow) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if r.State == "" {
		r.State = "pending"
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO proposals(workdir, goal, packages, created_at, state)
		VALUES(?,?,?,?,?)
		ON CONFLICT(workdir) DO UPDATE SET
			goal=excluded.goal, packages=excluded.packages, state=excluded.state`,
		r.Workdir, r.Goal, strings.Join(r.Packages, " "),
		r.CreatedAt.UTC().Format(time.RFC3339), r.State)
	return err
}

// PendingProposals lists prepared samples nobody has acted on, newest first.
func (d *DB) PendingProposals(ctx context.Context) ([]ProposalRow, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT workdir, goal, packages, created_at, state
		FROM proposals WHERE state='pending' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProposalRow
	for rows.Next() {
		var r ProposalRow
		var pkgs, created string
		if err := rows.Scan(&r.Workdir, &r.Goal, &pkgs, &created, &r.State); err != nil {
			return nil, err
		}
		if pkgs != "" {
			r.Packages = strings.Fields(pkgs)
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetProposalState marks a proposal published or dropped so it stops being
// offered for review.
func (d *DB) SetProposalState(ctx context.Context, workdir, state string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE proposals SET state=? WHERE workdir=?`, state, workdir)
	return err
}
