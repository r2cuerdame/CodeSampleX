package serverstore

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

// MovedDependency is one child that resolved to more than one version across
// the releases of a single parent.
//
// It is the shortest true answer this corpus can give to "what did you find
// out": a library moved under a release, which is where an upgrade changes
// something the reader did not choose. The home page led with inventory
// counters instead -- observations, samples, packages -- which say how big
// this network is, a fact about the network rather than about anything it
// learned.
type MovedDependency struct {
	Ecosystem  string
	ParentName string
	ChildName  string
	// Versions is how many distinct versions the child resolved to, and
	// Releases how many of the parent's releases were involved.
	Versions int
	Releases int
}

// MovedDependencyStore lists the children that moved.
type MovedDependencyStore interface {
	MovedDependencies(ctx context.Context, limit int) ([]MovedDependency, error)
}

// sortMovedDependencies orders by how much the child moved, then by name.
//
// Stable and total: the home page shows a handful of these and a reader who
// reloads must not get a different handful, so nothing is left to map order.
func sortMovedDependencies(rows []MovedDependency) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Versions != b.Versions {
			return a.Versions > b.Versions
		}
		if a.Ecosystem != b.Ecosystem {
			return a.Ecosystem < b.Ecosystem
		}
		if a.ParentName != b.ParentName {
			return a.ParentName < b.ParentName
		}
		return a.ChildName < b.ChildName
	})
}

// MovedDependencies lists children that resolved to more than one version.
func (f *Fake) MovedDependencies(_ context.Context, limit int) ([]MovedDependency, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	type key struct{ ecosystem, parent, child string }
	versions := map[key]map[string]bool{}
	releases := map[key]map[string]bool{}
	for e := range f.edges {
		k := key{e.ecosystem, e.parentName, e.childName}
		if versions[k] == nil {
			versions[k] = map[string]bool{}
			releases[k] = map[string]bool{}
		}
		versions[k][e.childVersion] = true
		releases[k][e.parentVersion] = true
	}
	var out []MovedDependency
	for k, vs := range versions {
		if len(vs) < 2 {
			continue
		}
		out = append(out, MovedDependency{
			Ecosystem: k.ecosystem, ParentName: k.parent, ChildName: k.child,
			Versions: len(vs), Releases: len(releases[k]),
		})
	}
	sortMovedDependencies(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MovedDependencies lists children that resolved to more than one version.
func (p *PG) MovedDependencies(ctx context.Context, limit int) ([]MovedDependency, error) {
	if limit <= 0 {
		limit = 10
	}
	var out []MovedDependency
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `
			SELECT ecosystem, parent_name, child_name,
			       count(DISTINCT child_version) AS versions,
			       count(DISTINCT parent_version) AS releases
			  FROM dependency_edge
			 GROUP BY 1, 2, 3
			HAVING count(DISTINCT child_version) > 1
			 ORDER BY versions DESC, ecosystem, parent_name, child_name
			 LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m MovedDependency
			if err := rows.Scan(&m.Ecosystem, &m.ParentName, &m.ChildName,
				&m.Versions, &m.Releases); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	// Ordered again in Go so both stores answer with one comparison rather
	// than one in SQL and one beside it.
	sortMovedDependencies(out)
	return out, nil
}
