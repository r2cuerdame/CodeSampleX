package search

import (
	"context"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// maxOnDemandKeys bounds one fetch. A caller names a handful of packages;
// a request that fans out further is not a search any more.
const maxOnDemandKeys = 4

// onDemandTimeout keeps a slow or unreachable server from turning a local
// search into a hang. A miss returned quickly beats an answer that arrives
// after the agent has moved on.
const onDemandTimeout = 8 * time.Second

// FetchMissing pulls the shards for packages the caller NAMED but this
// machine has never synced, and reports whether anything new arrived.
//
// It exists because of the shape of the actual question. `csx sync` warms
// the shards for packages already in the local inventory plus the server's
// HOT list — and the library an agent asks about is, in the normal case,
// one it is ABOUT TO ADD. That package is in neither list, so the network
// could hold a verified answer and this machine would return
// NO_SAFE_MATCH, advising a `csx sync` that would never fetch it.
//
// Only on a miss, only for packages the caller named, and never from the
// dependency tree: the tree is context, and fetching a shard names the
// package to the server.
//
// Community mode only. In local-only mode the whole point is that nothing
// about the caller's work leaves, and asking for a shard by name would
// announce the exact interest that mode exists to keep private — so a
// local-only install answers from what it has, and says so.
func FetchMissing(ctx context.Context, e Engine, sy *Syncer, mode string, req domain.SearchRequest) bool {
	if sy == nil || e.DB == nil || mode != "community" {
		return false
	}
	have := map[string]bool{}
	if rows, err := e.DB.ListShards(ctx); err == nil {
		for _, r := range rows {
			have[r.Key] = true
		}
	}
	var keys []string
	seen := map[string]bool{}
	for _, ps := range req.Packages {
		p, err := domain.ParsePURL(ps)
		if err != nil {
			continue
		}
		key := p.Ecosystem + "/" + p.Name + "/" + p.Major()
		if have[key] || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
		if len(keys) >= maxOnDemandKeys {
			break
		}
	}
	if len(keys) == 0 {
		return false
	}
	fctx, cancel := context.WithTimeout(ctx, onDemandTimeout)
	defer cancel()
	// Errors are ignored on purpose: a package the network has never heard
	// of 404s, and that is a normal answer rather than a failure worth
	// surfacing. What matters is whether anything arrived.
	n, _ := sy.SyncAll(fctx, keys)
	return n > 0
}
