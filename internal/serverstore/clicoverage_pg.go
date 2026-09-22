package serverstore

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ListCLIObservations is the PostgreSQL half of CLIObservationStore.
//
// The prefix predicate on purl walks evidence_agg_target_idx (purl, symbol),
// so this reads the CLI rows and nothing else; the OS comes out of the same
// env_json the authoring query reads it from. Ordered so a truncated read is
// a deterministic one.
func (p *PG) ListCLIObservations(ctx context.Context, limit int) ([]CLIObservationRow, error) {
	if limit <= 0 || limit > CLIObservationReadLimit {
		limit = CLIObservationReadLimit
	}
	var out []CLIObservationRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT purl, symbol, lower(COALESCE(env_json->>'os', '')), result,
			       observation_count, COALESCE(last_seen, first_seen, now())
			  FROM evidence_agg
			 WHERE purl LIKE 'pkg:generic/cli/%'
			 ORDER BY purl, symbol, env_hash
			 LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row CLIObservationRow
			var lastSeen time.Time
			if err := rows.Scan(&row.PURL, &row.Symbol, &row.OS, &row.Result, &row.Count, &lastSeen); err != nil {
				return err
			}
			row.OS = strings.TrimSpace(row.OS)
			row.LastSeen = lastSeen.UTC()
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

// FilterUnobservedCLIWork is the PostgreSQL half of CLIWorkCompletenessStore:
// one statement over the candidate window, each row an EXISTS on the (purl,
// symbol) index.
func (p *PG) FilterUnobservedCLIWork(ctx context.Context, rows []WantedRow, now time.Time) ([]WantedRow, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ordinals := make([]int64, 0, len(rows))
	purls := make([]string, 0, len(rows))
	farmSymbols := make([]string, 0, len(rows))
	fieldSymbols := make([]string, 0, len(rows))
	targetOS := make([]string, 0, len(rows))
	probes := make([]bool, 0, len(rows))
	kept := make([]WantedRow, 0, len(rows))
	for _, work := range rows {
		os, command, ok := domain.DecodeCLIWorkSymbol(work.Symbol)
		if !ok {
			continue
		}
		kept = append(kept, work)
		ordinals = append(ordinals, int64(len(kept)))
		targetOS = append(targetOS, os)
		if work.Version == "" {
			purls = append(purls, "pkg:generic/"+work.Name+"@%")
			probes = append(probes, true)
			farmSymbols = append(farmSymbols, "")
			fieldSymbols = append(fieldSymbols, "")
			continue
		}
		purls = append(purls, domain.PURL{Ecosystem: "generic", Name: work.Name, Version: work.Version}.String())
		probes = append(probes, false)
		farmSymbols = append(farmSymbols, domain.EncodeCLISymbol("", command, domain.ProvenanceFarm))
		fieldSymbols = append(fieldSymbols, domain.EncodeCLISymbol("", command, domain.ProvenanceField))
	}
	open := make(map[int64]bool, len(kept))
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		result, err := c.Query(ctx, `
			WITH candidate AS (
			  SELECT * FROM unnest($1::bigint[],$2::text[],$3::text[],$4::text[],$5::text[],$6::boolean[])
			    AS c(ordinal,purl,farm_symbol,field_symbol,target_os,probe)
			)
			SELECT ordinal FROM candidate c
			WHERE (c.probe
			       AND NOT EXISTS (SELECT 1 FROM evidence_agg e
			         WHERE e.purl LIKE c.purl AND e.symbol LIKE 'farm:%'
			           AND lower(COALESCE(e.env_json->>'os','')) = c.target_os
			           AND COALESCE(e.last_seen, e.first_seen) > $7))
			   OR (NOT c.probe
			       AND NOT EXISTS (SELECT 1 FROM evidence_agg e
			         WHERE e.purl = c.purl AND e.symbol IN (c.farm_symbol, c.field_symbol)
			           AND lower(COALESCE(e.env_json->>'os','')) = c.target_os))
			ORDER BY ordinal`, ordinals, purls, farmSymbols, fieldSymbols, targetOS, probes, now.Add(-CLIFarmReprobeAfter))
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var ordinal int64
			if err := result.Scan(&ordinal); err != nil {
				return err
			}
			open[ordinal] = true
		}
		return result.Err()
	})
	if err != nil {
		return nil, err
	}
	out := make([]WantedRow, 0, len(kept))
	for i, work := range kept {
		if open[int64(i+1)] {
			out = append(out, work)
		}
	}
	return out, nil
}
