package serverstore

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

// PackageAsset is what this network holds for one package, counted over its
// releases.
//
// Ratios, not flags. "This package has a sample" is true of a package with one
// sample and fifty releases, and a reader takes it to mean the package is
// covered -- the overstatement the three-axis census exists to prevent. It is
// also not an entry count: /records ranked packages by how many snapshot rows
// they had, which is a fact about this network's bookkeeping rather than about
// the package, and a library with 400 rows and no contract is less proven than
// one with three rows and a passing one.
type PackageAsset struct {
	Ecosystem string
	Name      string
	// Releases is every PUBLIC release of this package, the denominator the
	// other two are read against.
	Releases int
	// WithSample is releases a contract ran and passed at.
	WithSample int
	// WithDependency is releases whose dependency question has an answer of
	// either kind -- children recorded, or a resolver that read the release
	// and found none. Counting only the graphs would leave a closed
	// coordinate reading as outstanding work forever.
	WithDependency int
}

// PackageAssetStore rolls the release-level census up to packages.
type PackageAssetStore interface {
	PackageAssets(ctx context.Context) ([]PackageAsset, error)
}

// packageAssetsFrom folds classified releases into one row per package.
//
// Shared by both stores, and reading the same completenessRow the census and
// the gap list read: a package summary that disagreed with the coordinate rows
// underneath it would be a third opinion about the same corpus.
func packageAssetsFrom(rows []completenessRow) []PackageAsset {
	byPkg := map[[2]string]*PackageAsset{}
	for _, r := range rows {
		key := [2]string{r.ecosystem, r.name}
		a, ok := byPkg[key]
		if !ok {
			a = &PackageAsset{Ecosystem: r.ecosystem, Name: r.name}
			byPkg[key] = a
		}
		a.Releases++
		if r.sample {
			a.WithSample++
		}
		if r.dep != dependencyUnknown {
			a.WithDependency++
		}
	}
	out := make([]PackageAsset, 0, len(byPkg))
	for _, a := range byPkg {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// PackageAssets rolls every PUBLIC release up to its package.
func (f *Fake) PackageAssets(_ context.Context) ([]PackageAsset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return packageAssetsFrom(f.completenessRows()), nil
}

// PackageAssets rolls every PUBLIC release up to its package.
//
// The rollup runs in Go over the same classification the census and the gap
// list read, rather than as a third GROUP BY written to look like them.
func (p *PG) PackageAssets(ctx context.Context) ([]PackageAsset, error) {
	ctx, cancel := farmAggregateContext(ctx)
	defer cancel()
	var rows []completenessRow
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := beginFarmAggregate(ctx, c, farmAggregateTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		q, err := tx.Query(ctx, `
			WITH `+authoringCoverageCTE+`, `+completenessRelationsCTE+`
			SELECT `+completenessAxesSQL+`,
			       pk.ecosystem, pk.name, pk.version
			FROM packages pk
			WHERE `+completenessSubjectSQL)
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
		return nil, err
	}
	return packageAssetsFrom(rows), nil
}
