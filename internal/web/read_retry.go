package web

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// RouteMetrics tracks route health, store pressure, and absence verification.
// Metrics strictly separate proven absence from transient database/network errors.
type RouteMetrics struct {
	ProvenNotFound atomic.Int64
	DBQueryTimeout atomic.Int64
	PoolBusy       atomic.Int64
	RetryAttempted atomic.Int64
	RetryExhausted atomic.Int64
	Final503       atomic.Int64
	Final504       atomic.Int64
}

// RouteMetricsSnapshot provides a snapshot of current metrics counters.
type RouteMetricsSnapshot struct {
	ProvenNotFound int64
	DBQueryTimeout int64
	PoolBusy       int64
	RetryAttempted int64
	RetryExhausted int64
	Final503       int64
	Final504       int64
}

var defaultMetrics RouteMetrics

func recordProvenNotFound() { defaultMetrics.ProvenNotFound.Add(1) }
func recordDBQueryTimeout() { defaultMetrics.DBQueryTimeout.Add(1) }
func recordPoolBusy()       { defaultMetrics.PoolBusy.Add(1) }
func recordRetryAttempted() { defaultMetrics.RetryAttempted.Add(1) }
func recordRetryExhausted() { defaultMetrics.RetryExhausted.Add(1) }
func recordFinal503()       { defaultMetrics.Final503.Add(1) }
func recordFinal504()       { defaultMetrics.Final504.Add(1) }

// GetRouteMetrics returns a read-only snapshot of current route metrics.
func GetRouteMetrics() RouteMetricsSnapshot {
	return RouteMetricsSnapshot{
		ProvenNotFound: defaultMetrics.ProvenNotFound.Load(),
		DBQueryTimeout: defaultMetrics.DBQueryTimeout.Load(),
		PoolBusy:       defaultMetrics.PoolBusy.Load(),
		RetryAttempted: defaultMetrics.RetryAttempted.Load(),
		RetryExhausted: defaultMetrics.RetryExhausted.Load(),
		Final503:       defaultMetrics.Final503.Load(),
		Final504:       defaultMetrics.Final504.Load(),
	}
}

// ResetRouteMetrics resets all route metric counters to zero.
func ResetRouteMetrics() {
	defaultMetrics.ProvenNotFound.Store(0)
	defaultMetrics.DBQueryTimeout.Store(0)
	defaultMetrics.PoolBusy.Store(0)
	defaultMetrics.RetryAttempted.Store(0)
	defaultMetrics.RetryExhausted.Store(0)
	defaultMetrics.Final503.Store(0)
	defaultMetrics.Final504.Store(0)
}

const maxReadRetries = 2

type retryActiveKey struct{}

var (
	retryBaseBackoff = 20 * time.Millisecond
	testFastRetry    = false
	backoffMu        sync.RWMutex
)

func setFastRetryForTest(fast bool) {
	backoffMu.Lock()
	testFastRetry = fast
	backoffMu.Unlock()
}

func calculateBackoff(attempt int) time.Duration {
	backoffMu.RLock()
	fast := testFastRetry
	base := retryBaseBackoff
	backoffMu.RUnlock()

	if fast {
		return 1 * time.Millisecond
	}
	factor := 1 << attempt
	dur := base * time.Duration(factor)
	jitter := time.Duration(rand.Int63n(int64(base / 2)))
	return dur + jitter
}

func executeWithRetry[T any](ctx context.Context, opName string, fn func(ctx context.Context) (T, error)) (T, error) {
	// Prevent nested retry multiplication: if a parent read loop is already
	// retrying, perform this operation once.
	if ctx.Value(retryActiveKey{}) != nil {
		return fn(ctx)
	}
	ctx = context.WithValue(ctx, retryActiveKey{}, true)

	var (
		res T
		err error
	)
	for attempt := 0; attempt <= maxReadRetries; attempt++ {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res, err = fn(ctx)
		if err == nil {
			return res, nil
		}
		if !serverstore.IsTransientReadError(err) {
			return res, err
		}

		if serverstore.IsPoolBusy(err) {
			recordPoolBusy()
		}
		if serverstore.IsQueryTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			recordDBQueryTimeout()
		}

		if attempt == maxReadRetries {
			recordRetryExhausted()
			break
		}

		backoff := calculateBackoff(attempt)
		deadline, hasDeadline := ctx.Deadline()
		if hasDeadline && time.Until(deadline) <= backoff {
			recordRetryExhausted()
			break
		}

		recordRetryAttempted()

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return res, ctx.Err()
		}
	}
	return res, err
}

type retryStore struct {
	inner Store
}

func newRetryStore(s Store) Store {
	if s == nil {
		return nil
	}
	if rs, ok := s.(*retryStore); ok {
		return rs
	}
	return &retryStore{inner: s}
}

func (r *retryStore) LatestStatsJSON(ctx context.Context) (string, bool) {
	return r.inner.LatestStatsJSON(ctx)
}

func (r *retryStore) SnapshotJSON(ctx context.Context, purl, symbol string) (string, bool) {
	js, ok, _ := r.SnapshotJSONWithError(ctx, purl, symbol)
	return js, ok
}

func (r *retryStore) SnapshotJSONWithError(ctx context.Context, purl, symbol string) (string, bool, error) {
	type snapRes struct {
		json string
		ok   bool
	}
	v, err := executeWithRetry(ctx, "SnapshotJSON", func(c context.Context) (snapRes, error) {
		if pa, ok := r.inner.(interface {
			SnapshotJSONWithError(context.Context, string, string) (string, bool, error)
		}); ok {
			js, found, err := pa.SnapshotJSONWithError(c, purl, symbol)
			return snapRes{json: js, ok: found}, err
		}
		js, found := r.inner.SnapshotJSON(c, purl, symbol)
		return snapRes{json: js, ok: found}, nil
	})
	return v.json, v.ok, err
}

func (r *retryStore) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	return executeWithRetry(ctx, "PackageVersions", func(c context.Context) ([]string, error) {
		return r.inner.PackageVersions(c, ecosystem, name)
	})
}

func (r *retryStore) PackageSymbols(ctx context.Context, ecosystem, name, version string) ([]string, error) {
	return executeWithRetry(ctx, "PackageSymbols", func(c context.Context) ([]string, error) {
		return r.inner.PackageSymbols(c, ecosystem, name, version)
	})
}

func (r *retryStore) SymbolPackageSpread(ctx context.Context, ecosystem string, symbols []string) (map[string]int, error) {
	return executeWithRetry(ctx, "SymbolPackageSpread", func(c context.Context) (map[string]int, error) {
		return r.inner.SymbolPackageSpread(c, ecosystem, symbols)
	})
}

func (r *retryStore) SampleMeta(ctx context.Context, id string) (SampleMeta, bool, error) {
	type metaRes struct {
		meta SampleMeta
		ok   bool
	}
	v, err := executeWithRetry(ctx, "SampleMeta", func(c context.Context) (metaRes, error) {
		m, ok, err := r.inner.SampleMeta(c, id)
		return metaRes{meta: m, ok: ok}, err
	})
	return v.meta, v.ok, err
}

func (r *retryStore) SampleManifest(ctx context.Context, id string) (string, bool, error) {
	type manifestRes struct {
		manifest string
		ok       bool
	}
	v, err := executeWithRetry(ctx, "SampleManifest", func(c context.Context) (manifestRes, error) {
		m, ok, err := r.inner.SampleManifest(c, id)
		return manifestRes{manifest: m, ok: ok}, err
	})
	return v.manifest, v.ok, err
}

func (r *retryStore) SampleReceipts(ctx context.Context, id string) ([]string, error) {
	return executeWithRetry(ctx, "SampleReceipts", func(c context.Context) ([]string, error) {
		return r.inner.SampleReceipts(c, id)
	})
}

func (r *retryStore) SampleSource(ctx context.Context, id string) ([]SampleFile, error) {
	return executeWithRetry(ctx, "SampleSource", func(c context.Context) ([]SampleFile, error) {
		return r.inner.SampleSource(c, id)
	})
}

func (r *retryStore) SeederSamples(ctx context.Context, login string) ([]SampleListItem, error) {
	return executeWithRetry(ctx, "SeederSamples", func(c context.Context) ([]SampleListItem, error) {
		return r.inner.SeederSamples(c, login)
	})
}

func (r *retryStore) ListSamples(ctx context.Context, limit int) ([]SampleListItem, error) {
	return executeWithRetry(ctx, "ListSamples", func(c context.Context) ([]SampleListItem, error) {
		return r.inner.ListSamples(c, limit)
	})
}

func (r *retryStore) SamplesPage(ctx context.Context, offset, limit int) ([]SampleListItem, int, error) {
	type pageRes struct {
		items []SampleListItem
		total int
	}
	v, err := executeWithRetry(ctx, "SamplesPage", func(c context.Context) (pageRes, error) {
		items, total, err := r.inner.SamplesPage(c, offset, limit)
		return pageRes{items: items, total: total}, err
	})
	return v.items, v.total, err
}

func (r *retryStore) SearchSamples(ctx context.Context, query string, offset, limit int) ([]SampleListItem, int, error) {
	type searchRes struct {
		items []SampleListItem
		total int
	}
	v, err := executeWithRetry(ctx, "SearchSamples", func(c context.Context) (searchRes, error) {
		items, total, err := r.inner.SearchSamples(c, query, offset, limit)
		return searchRes{items: items, total: total}, err
	})
	return v.items, v.total, err
}

func (r *retryStore) PackageSamples(ctx context.Context, ecosystem, name string, limit int) ([]SampleListItem, error) {
	return executeWithRetry(ctx, "PackageSamples", func(c context.Context) ([]SampleListItem, error) {
		return r.inner.PackageSamples(c, ecosystem, name, limit)
	})
}

func (r *retryStore) ReleaseSamples(ctx context.Context, ecosystem, name, version string, limit int) ([]SampleListItem, error) {
	return executeWithRetry(ctx, "ReleaseSamples", func(c context.Context) ([]SampleListItem, error) {
		return r.inner.ReleaseSamples(c, ecosystem, name, version, limit)
	})
}

func (r *retryStore) PackageCodeCounts(ctx context.Context, ecosystem, name string) ([]PackageCodeCount, error) {
	return executeWithRetry(ctx, "PackageCodeCounts", func(c context.Context) ([]PackageCodeCount, error) {
		return r.inner.PackageCodeCounts(c, ecosystem, name)
	})
}

func (r *retryStore) SearchPackages(ctx context.Context, q string, limit int) ([]PackageHit, error) {
	return executeWithRetry(ctx, "SearchPackages", func(c context.Context) ([]PackageHit, error) {
		return r.inner.SearchPackages(c, q, limit)
	})
}

func (r *retryStore) HotPackages(ctx context.Context, limit int) ([]PackageHit, error) {
	return executeWithRetry(ctx, "HotPackages", func(c context.Context) ([]PackageHit, error) {
		return r.inner.HotPackages(c, limit)
	})
}

func (r *retryStore) RecordPackages(ctx context.Context, filter RecordFilter, offset, limit int) ([]PackageHit, int, error) {
	type recRes struct {
		hits  []PackageHit
		total int
	}
	v, err := executeWithRetry(ctx, "RecordPackages", func(c context.Context) (recRes, error) {
		hits, total, err := r.inner.RecordPackages(c, filter, offset, limit)
		return recRes{hits: hits, total: total}, err
	})
	return v.hits, v.total, err
}

func (r *retryStore) FailureClusters(ctx context.Context, ecosystem, name string) ([]string, int, error) {
	type clusterRes struct {
		clusters []string
		total    int
	}
	v, err := executeWithRetry(ctx, "FailureClusters", func(c context.Context) (clusterRes, error) {
		clusters, total, err := r.inner.FailureClusters(c, ecosystem, name)
		return clusterRes{clusters: clusters, total: total}, err
	})
	return v.clusters, v.total, err
}

func (r *retryStore) FailureIssueClusters(ctx context.Context, ecosystem, name string) ([]string, error) {
	return executeWithRetry(ctx, "FailureIssueClusters", func(c context.Context) ([]string, error) {
		return r.inner.FailureIssueClusters(c, ecosystem, name)
	})
}

func (r *retryStore) FailureIssueStagePasses(ctx context.Context, ecosystem, name, stage string) (map[string]int64, error) {
	return executeWithRetry(ctx, "FailureIssueStagePasses", func(c context.Context) (map[string]int64, error) {
		return r.inner.FailureIssueStagePasses(c, ecosystem, name, stage)
	})
}

func (r *retryStore) TopWanted(ctx context.Context, limit int) ([]WantedRow, error) {
	return executeWithRetry(ctx, "TopWanted", func(c context.Context) ([]WantedRow, error) {
		return r.inner.TopWanted(c, limit)
	})
}

func (r *retryStore) WantedForPackage(ctx context.Context, ecosystem, name string) ([]WantedRow, error) {
	return executeWithRetry(ctx, "WantedForPackage", func(c context.Context) ([]WantedRow, error) {
		return r.inner.WantedForPackage(c, ecosystem, name)
	})
}

func (r *retryStore) Dependencies(ctx context.Context, ecosystem, name string) ([]DependencyEdge, error) {
	return executeWithRetry(ctx, "Dependencies", func(c context.Context) ([]DependencyEdge, error) {
		return r.inner.Dependencies(c, ecosystem, name)
	})
}

func (r *retryStore) FailureIssueDependencies(ctx context.Context, ecosystem, name, fingerprint string) ([]DependencyEdge, error) {
	return executeWithRetry(ctx, "FailureIssueDependencies", func(c context.Context) ([]DependencyEdge, error) {
		return r.inner.FailureIssueDependencies(c, ecosystem, name, fingerprint)
	})
}

func (r *retryStore) DependencySubjects(ctx context.Context, query string, offset, limit int) ([]DependencySubject, int, error) {
	type subRes struct {
		rows  []DependencySubject
		total int
	}
	v, err := executeWithRetry(ctx, "DependencySubjects", func(c context.Context) (subRes, error) {
		rows, total, err := r.inner.DependencySubjects(c, query, offset, limit)
		return subRes{rows: rows, total: total}, err
	})
	return v.rows, v.total, err
}

func (r *retryStore) DependencyParents(ctx context.Context, ecosystem, name, version string) ([]DependencyEdge, error) {
	return executeWithRetry(ctx, "DependencyParents", func(c context.Context) ([]DependencyEdge, error) {
		return r.inner.DependencyParents(c, ecosystem, name, version)
	})
}

func (r *retryStore) DependencyResolvedNone(ctx context.Context, ecosystem, name, version string) (bool, error) {
	return executeWithRetry(ctx, "DependencyResolvedNone", func(c context.Context) (bool, error) {
		return r.inner.DependencyResolvedNone(c, ecosystem, name, version)
	})
}

func (r *retryStore) PackageAssets(ctx context.Context) ([]PackageAsset, error) {
	return executeWithRetry(ctx, "PackageAssets", func(c context.Context) ([]PackageAsset, error) {
		return r.inner.PackageAssets(c)
	})
}

func (r *retryStore) CompletenessGaps(ctx context.Context, query string, offset, limit int) ([]CompletenessGap, int, error) {
	type gapRes struct {
		rows  []CompletenessGap
		total int
	}
	v, err := executeWithRetry(ctx, "CompletenessGaps", func(c context.Context) (gapRes, error) {
		rows, total, err := r.inner.CompletenessGaps(c, query, offset, limit)
		return gapRes{rows: rows, total: total}, err
	})
	return v.rows, v.total, err
}

func (r *retryStore) DerivedFindings(ctx context.Context) ([]DerivedFinding, error) {
	return executeWithRetry(ctx, "DerivedFindings", func(c context.Context) ([]DerivedFinding, error) {
		return r.inner.DerivedFindings(c)
	})
}
