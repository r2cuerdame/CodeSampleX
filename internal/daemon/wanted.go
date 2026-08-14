package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// maxWantedPackages bounds one report. A search carries the caller's whole
// dependency tree as context, and filing all of it would let one large
// project dominate the ranking; the packages the question was about are
// the ones the caller named.
const maxWantedPackages = 10

// reportWanted tells the network that it had no answer, so the question
// can be counted and eventually answered by someone.
//
// Every NO_SAFE_MATCH used to be thrown away. It was counted locally, on
// the one machine that already knew, where nobody could act on it —
// while aggregated it is the most useful thing this network can tell a
// contributor: here is what people keep asking that nobody has answered.
//
// THE QUESTION IS NEVER SENT. A typed question carries project names, file
// paths, sometimes an error string with a hostname in it, and goal.md §8.5
// keeps all of that on the machine. What travels is the part of the request
// that was already public and already uploaded as evidence: which package,
// and which symbol if one was named.
//
// Community mode only, like every other upload, and best-effort: a search
// must not fail, slow down or wait because a report could not be sent.
func (d *Daemon) reportWanted(ctx context.Context, req domain.SearchRequest) {
	if d.Cfg.Mode != "community" || d.Ident == nil {
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
		if seen[key] {
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
	body, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"epoch":         epoch,
		"anonId":        d.Ident.AnonID(epoch),
		"packages":      pkgs,
		"symbols":       req.Symbols,
	})
	if err != nil {
		return
	}
	url := strings.TrimRight(d.Cfg.ServerURL, "/") + "/v1/wanted"
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	hreq, err := http.NewRequestWithContext(rctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient().Do(hreq)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}
