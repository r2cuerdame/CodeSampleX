package serverstore

import (
	"context"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// DependencySubject is one release that other packages resolved onto.
//
// The atlas reads the dependency graph from the child's side, which is the
// side nobody could browse before: Dependencies answers "what did this package
// pull", and every consumer of the map had to already know a parent to ask.
// The question people actually arrive with — "who pulls this, and at which
// version" — had no entry point at all.
//
// Parents counts distinct parent RELEASES rather than parent names, because
// "four releases of one library pulled this" and "four different libraries
// pulled this" are different facts and the second is the interesting one; the
// name count is recoverable from the parent list, the release count is not.
type DependencySubject struct {
	Ecosystem string
	Name      string
	Version   string
	// Parents is how many distinct (name, version) pairs resolved onto this
	// release.
	Parents int
	// Projects is how many distinct project-days recorded any of those edges.
	// dependency_edge's key includes bucket and epoch, so a row IS a
	// project-day and counting rows counts them — the same arithmetic
	// Dependencies uses, kept identical so the two surfaces cannot disagree
	// about one edge.
	Projects int
}

// DependencySubjects returns one ranked page of dependency subjects, plus how
// many match the query in total.
//
// Ranked by projects first: a release a hundred lockfiles resolved onto is
// more worth a reader's attention than one a single project pulled, and that
// is a measurement rather than a preference. The query matches the child name
// only; matching versions would let "1.0" pull in every package that happens
// to have such a release, which is noise dressed as a search.
//
// position() rather than ILIKE. Built as '%' || $1 || '%', a query containing
// % or _ became a pattern: "a_c" matched "abc" and a bare "%" matched
// everything. Nobody typing an underscore into a search box means "any
// character", and the Fake's strings.Contains never did -- so the two stores
// also disagreed.
//
// The order ends at ecosystem, and it has to end somewhere total. Two
// ecosystems can hold the same name at the same version; tied, PostgreSQL may
// return them either way round between one page and the next, and the row on
// the boundary is then shown twice or not at all.
func (p *PG) DependencySubjects(ctx context.Context, query string, offset, limit int) ([]DependencySubject, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := strings.TrimSpace(query)
	var out []DependencySubject
	total := 0
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT ecosystem, child_name, child_version,
			       count(DISTINCT parent_name || '@' || parent_version) AS parents,
			       count(*) AS projects,
			       count(*) OVER () AS full_count
			  FROM dependency_edge
			 WHERE ($1 = '' OR position(lower($1) in lower(child_name)) > 0)
			 GROUP BY 1, 2, 3
			 ORDER BY projects DESC, parents DESC, child_name, child_version, ecosystem
			 LIMIT $2 OFFSET $3`, q, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s DependencySubject
			var fullCount int
			if err := rows.Scan(&s.Ecosystem, &s.Name, &s.Version, &s.Parents, &s.Projects, &fullCount); err != nil {
				return err
			}
			total = fullCount
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) == 0 && offset > 0 {
			if err := c.QueryRow(ctx, `
				SELECT count(*) FROM (
				  SELECT 1 FROM dependency_edge
				   WHERE ($1 = '' OR position(lower($1) in lower(child_name)) > 0)
				   GROUP BY ecosystem, child_name, child_version) t`, q).Scan(&total); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// DependencyParents lists the releases that resolved onto one exact release.
//
// The exact version on both ends, never a name alone: "something in this
// library pulled something in that one" is not a fact anybody can act on, and
// the version that moved under an upgrade is the whole question.
func (p *PG) DependencyParents(ctx context.Context, ecosystem, name, version string) ([]DependencyEdge, error) {
	var out []DependencyEdge
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT parent_name, parent_version, child_name, child_version, count(*) AS projects
			  FROM dependency_edge
			 WHERE ecosystem = $1 AND child_name = $2 AND child_version = $3
			 GROUP BY 1, 2, 3, 4`, ecosystem, name, version)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e DependencyEdge
			if err := rows.Scan(&e.ParentName, &e.ParentVersion, &e.ChildName, &e.ChildVersion, &e.Projects); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	sortDependencyParents(out)
	return out, nil
}

// DependencySubjects is the Fake's half. It exists so the two stores can be
// held to the same answer: a browse surface that disagrees with the store
// behind it is the failure mode this project keeps finding in production
// rather than in tests.
func (f *Fake) DependencySubjects(_ context.Context, query string, offset, limit int) ([]DependencySubject, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := strings.ToLower(strings.TrimSpace(query))

	f.mu.Lock()
	defer f.mu.Unlock()
	type key struct{ ecosystem, name, version string }
	parents := map[key]map[string]bool{}
	projects := map[key]int{}
	for k, projectDays := range f.edges {
		if q != "" && !strings.Contains(strings.ToLower(k.childName), q) {
			continue
		}
		id := key{k.ecosystem, k.childName, k.childVersion}
		if parents[id] == nil {
			parents[id] = map[string]bool{}
		}
		parents[id][k.parentName+"@"+k.parentVersion] = true
		// The project-days this edge was seen on, not the edge. PostgreSQL
		// counts dependency_edge rows and that table's key includes bucket and
		// epoch, so a row IS a project-day -- incrementing once per edge made
		// the Fake rank the same corpus differently from the store the site
		// actually reads, on the very number the atlas is ordered by.
		projects[id] += len(projectDays)
	}
	all := make([]DependencySubject, 0, len(parents))
	for id, ps := range parents {
		all = append(all, DependencySubject{
			Ecosystem: id.ecosystem, Name: id.name, Version: id.version,
			Parents: len(ps), Projects: projects[id],
		})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Projects != all[j].Projects {
			return all[i].Projects > all[j].Projects
		}
		if all[i].Parents != all[j].Parents {
			return all[i].Parents > all[j].Parents
		}
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		if all[i].Version != all[j].Version {
			return all[i].Version < all[j].Version
		}
		return all[i].Ecosystem < all[j].Ecosystem
	})
	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
}

// DependencyParents is the Fake's half of the parent lookup.
func (f *Fake) DependencyParents(_ context.Context, ecosystem, name, version string) ([]DependencyEdge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []DependencyEdge
	for k, projectDays := range f.edges {
		if k.ecosystem != ecosystem || k.childName != name || k.childVersion != version {
			continue
		}
		out = append(out, DependencyEdge{
			ParentName: k.parentName, ParentVersion: k.parentVersion,
			ChildName: k.childName, ChildVersion: k.childVersion,
			Projects: len(projectDays),
		})
	}
	sortDependencyParents(out)
	return out, nil
}

// sortDependencyParents orders the parents of one release: the most widely
// observed first, then stable by name and version so a page does not reshuffle
// between two reads of the same data.
func sortDependencyParents(edges []DependencyEdge) {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Projects != edges[j].Projects {
			return edges[i].Projects > edges[j].Projects
		}
		if edges[i].ParentName != edges[j].ParentName {
			return edges[i].ParentName < edges[j].ParentName
		}
		return edges[i].ParentVersion < edges[j].ParentVersion
	})
}

// DependencyResolvedNone reports whether some resolution measured this exact
// release to declare no dependencies at all.
//
// It is the difference between two things a blank row cannot tell apart: a
// release whose tree was read and found empty, and one nothing has ever read.
// The first is an answer and closes the dependency axis for that coordinate;
// the second is a gap and must stay on the board.
//
// A row only exists when something actually looked — an ecosystem with no edge
// scanner produces silence rather than a leaf claim (#152) — so presence here
// is a measurement rather than an absence of evidence.
func (p *PG) DependencyResolvedNone(ctx context.Context, ecosystem, name, version string) (bool, error) {
	found := false
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM dependency_resolution
			                WHERE ecosystem = $1 AND name = $2 AND version = $3)`,
			ecosystem, name, version).Scan(&found)
	})
	return found, err
}

// DependencyResolvedNone is the Fake's half.
func (f *Fake) DependencyResolvedNone(_ context.Context, ecosystem, name, version string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolvedNone[[3]string{ecosystem, name, version}], nil
}
