package evidence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// maxWantedPackages bounds one report. A search carries the caller's whole
// dependency tree as context, and filing all of it would let one large
// project dominate the ranking; the packages the question was about are
// the ones the caller named.
const maxWantedPackages = 10

// QueueWanted records that the network had no answer, so the question can
// be counted and eventually answered by someone.
//
// It goes through the upload queue rather than straight to the network:
// the queue retries, works offline, and a search must never wait on a
// report. It lives here rather than in the daemon because AGENTS ARRIVE
// OVER MCP — the daemon's HTTP search is not the path that matters, and
// wiring the signal only there meant the one caller who actually asks
// questions was the one caller whose misses were thrown away.
//
// THE QUESTION IS NEVER SENT. A typed question carries project names, file
// paths, sometimes an error string with a hostname in it, and goal.md §8.5
// keeps all of that on the machine. What travels is the part of the
// request that was already public: which package, and which symbol if one
// was named.
func QueueWanted(ctx context.Context, db *localdb.DB, ident *identity.Identity,
	cfg *config.Config, req domain.SearchRequest) {
	if db == nil || ident == nil || cfg == nil || cfg.Mode != config.ModeCommunity {
		return
	}
	// Packages the caller NAMED. ProjectPackages is the lockfile — context,
	// not the question — and counting it would rank whatever is popular in
	// dependency trees rather than what people are stuck on.
	pkgs := make([]string, 0, maxWantedPackages)
	seen := map[string]bool{}
	for _, ps := range req.Packages {
		p, err := domain.ParsePURL(ps)
		if err != nil {
			continue
		}
		key := p.Ecosystem + "/" + p.Name
		if seen[key] || cfg.IsExcluded(ps, p.Ecosystem, p.Name) {
			continue
		}
		seen[key] = true
		pkgs = append(pkgs, ps)
		if len(pkgs) >= maxWantedPackages {
			break
		}
	}
	if len(pkgs) == 0 {
		return // nothing public to report; the question stays on this machine
	}

	epoch := time.Now().UTC().Format("2006-01-02")
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"epoch":         epoch,
		"anonId":        ident.AnonID(epoch),
		"packages":      pkgs,
		"symbols":       req.Symbols,
	})
	if err != nil {
		return
	}
	_, _ = db.Enqueue(ctx, "wanted", string(payload))
}
