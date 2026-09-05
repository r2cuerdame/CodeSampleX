package serverstore

import (
	"context"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The dependency axis's three answers, in the words that reach the page.
//
// Strings rather than the private dependencyState so the web layer can render
// them without importing the census's internals, and so a new answer is a
// compile-time addition rather than an integer that silently means "unknown".
const (
	// DependencyGapUnknown: nothing has resolved this release. NOT "it has no
	// dependencies".
	DependencyGapUnknown = "unknown"
	// DependencyGapGraph: a resolution named this release's children.
	DependencyGapGraph = "graph"
	// DependencyGapProvenNone: a resolution ran and found no children.
	DependencyGapProvenNone = "none"
)

// The census and the /gaps listing read one classification, written once.
//
// Two SQL statements that mean to say the same thing about completeness is
// how a page comes to report work that never shrinks however hard the farm
// runs. The Go halves share completenessRow; these are the PostgreSQL halves
// of the same promise.
const (
	// completenessCoverageCTE provides verified packages for the sample axis.
	// It avoids materializing the heavy dependency_open closure which is only needed
	// by authoring queue scheduling, not completeness auditing.
	completenessCoverageCTE = `verified_packages AS MATERIALIZED (
				SELECT DISTINCT sp.purl
				FROM sample_packages sp
				JOIN samples s ON s.sample_id = sp.sample_id
				WHERE NOT s.quarantined
				  AND EXISTS (SELECT 1 FROM receipts r WHERE r.sample_id = s.sample_id AND r.contract_result = 'PASS')
			)`

	// completenessRelationsCTE names the two ways a release's dependencies
	// can have been answered.
	completenessRelationsCTE = `dependency_edge_parents AS MATERIALIZED (
				-- The PARENT end, deliberately. Being pulled BY somebody says
				-- nothing about what this release pulls, and the dependency
				-- axis is the second question.
				SELECT DISTINCT 'pkg:'||ecosystem||'/'||
				         CASE WHEN left(parent_name,1)='@'
				              THEN '%40'||substring(parent_name from 2)
				              ELSE parent_name END||'@'||parent_version AS purl
				FROM dependency_edge
			), resolved_none AS MATERIALIZED (
				-- A release a resolver read and found nothing in. It can never
				-- be a parent, so without this every leaf stayed open forever:
				-- 490 coordinates on production appear as a child of some
				-- resolved tree and never as a parent.
				SELECT DISTINCT 'pkg:'||ecosystem||'/'||
				         CASE WHEN left(name,1)='@'
				              THEN '%40'||substring(name from 2)
				              ELSE name END||'@'||version AS purl
				FROM dependency_resolution
			), observed_packages AS MATERIALIZED (
				SELECT pk.purl
				  FROM packages pk
				 WHERE ` + completenessSubjectSQL + `
				   AND EXISTS (SELECT 1 FROM evidence_agg e WHERE e.purl = pk.purl)
			)`

	// completenessClassifiedSQL runs the single-pass multi-axis classification
	// using hash joins instead of nested scalar subqueries.
	completenessClassifiedSQL = `
			WITH ` + completenessCoverageCTE + `, ` + completenessRelationsCTE + `,
			classified AS MATERIALIZED (
				SELECT (CASE WHEN v.purl IS NOT NULL THEN 'S' ELSE '-' END)
				    || (CASE WHEN v.purl IS NOT NULL OR obs.purl IS NOT NULL THEN 'E' ELSE '-' END)
				    || (CASE WHEN dep.purl IS NOT NULL OR rn.purl IS NOT NULL THEN 'D' ELSE '-' END) AS state,
				       (rn.purl IS NOT NULL AND dep.purl IS NULL) AS proven_none,
				       pk.ecosystem, pk.name, pk.version
				  FROM packages pk
				  LEFT JOIN verified_packages v ON v.purl = pk.purl
				  LEFT JOIN dependency_edge_parents dep ON dep.purl = pk.purl
				  LEFT JOIN resolved_none rn ON rn.purl = pk.purl
				  LEFT JOIN observed_packages obs ON obs.purl = pk.purl
				 WHERE ` + completenessSubjectSQL + `
			)`

	// completenessSubjectSQL is the corpus both read: every public release.
	completenessSubjectSQL = `pk.version<>'' AND pk.publicness='PUBLIC'`
)

// CompletenessGap is one coordinate that is missing at least one of Sample,
// Evidence and Dependency, with the reason each missing axis is missing.
//
// The census (FarmCompleteness) counts these into eight cells. It could say
// two thirds of the corpus was incomplete and not which two thirds, so the
// number was true and unactionable. This is the same reading, listed.
type CompletenessGap struct {
	Ecosystem string
	Name      string
	Version   string

	HasSample   bool
	HasEvidence bool
	// Dependency is one of the DependencyGap* constants.
	Dependency string

	// SampleNAReason and DependencyNAReason are non-empty when this network
	// cannot close that axis at all -- the authoring queue's own sentence, so
	// a contributor is not handed work every poll will decline.
	//
	// A gap with a reason is still listed. Hiding it would make the page
	// disagree with the census, which subtracts these from the backlog but
	// keeps counting them.
	SampleNAReason     string
	DependencyNAReason string
}

// State renders the three axes as the census cell this coordinate falls in,
// so a row on the page can be traced to a column in the matrix.
func (g CompletenessGap) State() string {
	dep := dependencyUnknown
	switch g.Dependency {
	case DependencyGapGraph:
		dep = dependencyGraph
	case DependencyGapProvenNone:
		dep = dependencyProvenNone
	}
	return completenessKey(g.HasSample, g.HasEvidence, dep)
}

// held counts the axes this coordinate has, which is the order the page reads
// in: the emptiest coordinate is the most work left.
func (g CompletenessGap) held() int {
	n := 0
	if g.HasSample {
		n++
	}
	if g.HasEvidence {
		n++
	}
	if g.Dependency != DependencyGapUnknown {
		n++
	}
	return n
}

// CompletenessGapStore lists the coordinates behind the census.
type CompletenessGapStore interface {
	CompletenessGaps(ctx context.Context, query string, offset, limit int) ([]CompletenessGap, int, error)
}

// completenessRow is one PUBLIC release with its three axes decided.
type completenessRow struct {
	ecosystem, name, version string
	sample, evidence         bool
	dep                      dependencyState
}

// gap renders one classified row as a listable gap, and says whether it is
// one at all.
//
// This is the census's own admission rule (FarmCompleteness.add) read back
// out: a coordinate unaskable on both axes is not backlog and is not in
// States, so it is not on this page either. Everything States counts outside
// SED is here, one row per coordinate.
func (r completenessRow) gap() (CompletenessGap, bool) {
	g := CompletenessGap{
		Ecosystem: r.ecosystem, Name: r.name, Version: r.version,
		HasSample: r.sample, HasEvidence: r.evidence,
		Dependency: DependencyGapUnknown,
	}
	switch r.dep {
	case dependencyGraph:
		g.Dependency = DependencyGapGraph
	case dependencyProvenNone:
		g.Dependency = DependencyGapProvenNone
	}
	if reason, na := domain.SampleNotApplicable(r.ecosystem, r.name); na {
		g.SampleNAReason = reason
	}
	if reason, na := domain.DependencyNotApplicable(r.ecosystem); na {
		g.DependencyNAReason = reason
	}
	// Two N/A axes justify those two absences, never a missing Evidence
	// record. Keep the coordinate until something has actually run.
	if g.SampleNAReason != "" && g.DependencyNAReason != "" && g.HasEvidence {
		return CompletenessGap{}, false
	}
	if g.State() == "SED" {
		return CompletenessGap{}, false
	}
	return g, true
}

// completenessGapsFrom pages classified rows into the answer, emptiest first.
//
// Shared by both stores so the ordering, the filter and the admission rule
// cannot differ between them -- the divergence class that has already shipped
// twice on this axis.
func completenessGapsFrom(rows []completenessRow, query string, offset, limit int) ([]CompletenessGap, int) {
	q := strings.ToLower(strings.TrimSpace(query))
	var gaps []CompletenessGap
	for _, r := range rows {
		if q != "" && !strings.Contains(strings.ToLower(r.name), q) {
			continue
		}
		if g, ok := r.gap(); ok {
			gaps = append(gaps, g)
		}
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		a, b := gaps[i], gaps[j]
		if a.held() != b.held() {
			return a.held() < b.held()
		}
		if a.Ecosystem != b.Ecosystem {
			return a.Ecosystem < b.Ecosystem
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Version < b.Version
	})
	total := len(gaps)
	if offset >= total {
		return nil, total
	}
	gaps = gaps[offset:]
	if limit > 0 && len(gaps) > limit {
		gaps = gaps[:limit]
	}
	return gaps, total
}

// CompletenessGaps lists the coordinates the census counts as incomplete.
func (f *Fake) CompletenessGaps(_ context.Context, query string, offset, limit int) ([]CompletenessGap, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	gaps, total := completenessGapsFrom(f.completenessRows(), query, offset, limit)
	return gaps, total, nil
}

// CompletenessGaps lists the coordinates the census counts as incomplete.
//
// The classification is SQL's and the admission rule is Go's, deliberately.
// SQL decides the three axes because that is where the corpus lives; the
// judgement about what is backlog -- which unaskable coordinates leave the
// list, and in what order the rest read -- is completenessGapsFrom, the same
// function the Fake calls. Writing the applicability rules a second time in
// SQL is how the two stores drift apart, and this axis has drifted twice.
func (p *PG) CompletenessGaps(ctx context.Context, query string, offset, limit int) ([]CompletenessGap, int, error) {
	ctx, cancel := farmAggregateContext(ctx)
	defer cancel()
	var rows []completenessRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := beginFarmAggregate(ctx, c, farmAggregateTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		// SED is dropped here rather than in Go: it is the large majority of
		// the corpus on a healthy network, and the page never shows it.
		q, err := tx.Query(ctx, completenessClassifiedSQL+`
			SELECT state, proven_none, ecosystem, name, version
			FROM classified
			WHERE state <> 'SED'`)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var state, ecosystem, name, version string
			var provenNone bool
			if err := q.Scan(&state, &provenNone, &ecosystem, &name, &version); err != nil {
				return err
			}
			dep := dependencyUnknown
			switch {
			case provenNone:
				dep = dependencyProvenNone
			case state[2] == 'D':
				dep = dependencyGraph
			}
			rows = append(rows, completenessRow{
				ecosystem: ecosystem, name: name, version: version,
				sample: state[0] == 'S', evidence: state[1] == 'E', dep: dep,
			})
		}
		return q.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	gaps, total := completenessGapsFrom(rows, query, offset, limit)
	return gaps, total, nil
}
