package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

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

// WantedCandidateQueueKind is deliberately not the legacy "wanted" kind.
// Older daemons know how to POST "wanted" rows without a client-side
// publicness recheck.  Giving unverified local candidates a new kind means
// an upgraded MCP process can safely share a home with an older daemon: the
// older process leaves the unfamiliar row alone instead of transmitting it.
const WantedCandidateQueueKind = "wanted_candidate"

// ErrWantedPublicnessUnconfirmed means a candidate was syntactically safe to
// keep locally, but no package in it was confirmed public.  The queue drainer
// treats this as retryable because a registry outage and a private/nonexistent
// coordinate are intentionally indistinguishable at this privacy boundary.
var ErrWantedPublicnessUnconfirmed = errors.New("wanted package publicness is unconfirmed")

// WantedReport is the complete privacy-reduced wire shape. It contains no
// prose query, project path, dependency-tree context, hostname, or raw log.
// Candidates use the same shape locally, but are not eligible for upload
// until PrepareWantedForUpload confirms and rewrites their package list.
type WantedReport struct {
	SchemaVersion int      `json:"schemaVersion"`
	Epoch         string   `json:"epoch"`
	AnonID        string   `json:"anonId"`
	Packages      []string `json:"packages"`
	Symbols       []string `json:"symbols,omitempty"`
	// OS is the platform the miss happened on, optional: a report without
	// one is a question about the package rather than about a platform. It
	// is what lets the work queue hand a Windows ask to a Windows verifier
	// instead of closing it with a Linux pass.
	OS string `json:"os,omitempty"`
}

// wantedReportOSes is the entire vocabulary a report may name — the platforms
// verification can actually target, matching the server's own list. A
// free-text environment string against a stable anonymous id is a
// fingerprint, so the coarse platform name is all that may travel.
var wantedReportOSes = map[string]bool{"linux": true, "windows": true, "darwin": true}

// wantedReportOS normalizes an OS to the wire vocabulary, and to "" for
// anything outside it — fail-closed, like every other field on this path.
func wantedReportOS(raw string) string {
	os := strings.ToLower(strings.TrimSpace(raw))
	if wantedReportOSes[os] {
		return os
	}
	return ""
}

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
// keeps all of that on the machine. The local candidate contains only
// package coordinates and an unambiguous symbol; the daemon still must
// confirm the exact release on its public registry before any of it is
// eligible to travel.
func QueueWanted(ctx context.Context, db *localdb.DB, ident *identity.Identity,
	cfg *config.Config, req domain.SearchRequest) {
	if db == nil || ident == nil || cfg == nil || cfg.Mode != config.ModeCommunity {
		return
	}
	// Packages the caller NAMED. ProjectPackages is the lockfile — context,
	// not the question — and counting it would rank whatever is popular in
	// dependency trees rather than what people are stuck on. This stage is
	// intentionally local-only and performs no registry request: returning
	// NO_SAFE_MATCH must not wait on third-party network latency.
	pkgs := make([]string, 0, maxWantedPackages)
	seen := map[string]bool{}
	derivedTargetCount := 0
	appendPackage := func(p domain.PURL) {
		key := p.Ecosystem + "/" + p.Name + "@" + p.Version
		if seen[key] || cfg.IsExcluded(p.String(), p.Ecosystem, p.Name) || len(pkgs) >= maxWantedPackages {
			return
		}
		seen[key] = true
		pkgs = append(pkgs, p.String())
	}
	for _, ps := range req.Packages {
		p, err := domain.ParsePURL(ps)
		if err != nil {
			continue
		}
		if !wantedEcosystem(p.Ecosystem) || !domain.ConcreteResolvedVersion(p.Version) ||
			(p.Ecosystem == "generic" && !domain.IsWantedTarget(p)) {
			continue
		}
		appendPackage(p)
		if len(pkgs) >= maxWantedPackages {
			break
		}
	}
	// A request can be about the engine or SDK itself and name no registry
	// package. Only the fixed public vocabulary is converted; arbitrary
	// framework strings remain local.
	if len(req.Packages) == 0 && (req.Environment.Ecosystem == "" || req.Environment.Ecosystem == "generic") {
		for _, framework := range req.Environment.Frameworks {
			if p, ok := domain.WantedTargetFromFramework(framework); ok {
				derivedTargetCount++
				appendPackage(p)
			}
		}
	}
	if len(pkgs) == 0 {
		return // nothing eligible to check later; the question stays local
	}

	epoch := time.Now().UTC().Format("2006-01-02")
	// Symbols in SearchRequest are global. They are attributable only when
	// the caller named exactly one package; otherwise attaching them would
	// invent a package/symbol relationship.
	var symbols []string
	if len(pkgs) == 1 && ((len(req.Packages) == 1 && derivedTargetCount == 0) ||
		(len(req.Packages) == 0 && derivedTargetCount == 1)) {
		symbols = sanitizeWantedSymbols(req.Symbols)
	}
	payload, err := json.Marshal(WantedReport{
		SchemaVersion: 1,
		Epoch:         epoch,
		AnonID:        ident.AnonID(epoch),
		Packages:      pkgs,
		Symbols:       symbols,
		OS:            wantedReportOS(req.Environment.OS),
	})
	if err != nil {
		return
	}
	_, _ = db.Enqueue(ctx, WantedCandidateQueueKind, string(payload))
}

// PrepareWantedForUpload applies the last, fail-closed privacy gate at the
// point where a queued candidate is about to leave the machine. It decodes
// into the fixed wire type and marshals it again, so even a hand-edited queue
// row cannot smuggle an unknown query/path/log field through the daemon.
//
// isPublic must check the exact package version. A nil checker or a false
// result never transmits that coordinate. The server repeats this check on
// receipt; neither side treats PURL syntax as proof of public existence.
func PrepareWantedForUpload(ctx context.Context, payload string,
	isPublic func(context.Context, domain.PURL) bool) ([]byte, error) {
	if isPublic == nil {
		return nil, ErrWantedPublicnessUnconfirmed
	}
	var report WantedReport
	if err := json.Unmarshal([]byte(payload), &report); err != nil {
		return nil, fmt.Errorf("decode wanted candidate: %w", err)
	}
	if report.SchemaVersion != 1 || report.Epoch == "" || report.AnonID == "" ||
		len(report.Packages) == 0 || len(report.Packages) > maxWantedPackages {
		return nil, fmt.Errorf("invalid wanted candidate envelope")
	}

	// Preserve the request's original ambiguity. If it named multiple
	// packages, filtering private coordinates down to one must not suddenly
	// make global symbols look attributable to the survivor.
	if len(report.Packages) != 1 {
		report.Symbols = nil
	} else {
		report.Symbols = sanitizeWantedSymbols(report.Symbols)
	}
	// A hand-edited queue row must not smuggle a free-text environment
	// string past this gate — and the server 400s a whole BATCH on an
	// unknown os, so a poisoned row is cleaned here rather than left to
	// block every report travelling with it.
	report.OS = wantedReportOS(report.OS)

	confirmed := make([]string, 0, len(report.Packages))
	seen := map[string]bool{}
	for _, raw := range report.Packages {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrWantedPublicnessUnconfirmed, err)
		}
		p, err := domain.ParsePURL(raw)
		if err != nil || !wantedEcosystem(p.Ecosystem) || !domain.ConcreteResolvedVersion(p.Version) ||
			(p.Ecosystem == "generic" && !domain.IsWantedTarget(p)) {
			continue
		}
		canonical := p.String()
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		if domain.IsWantedTarget(p) || isPublic(ctx, p) {
			confirmed = append(confirmed, canonical)
		}
	}
	if len(confirmed) == 0 {
		return nil, ErrWantedPublicnessUnconfirmed
	}
	report.Packages = confirmed
	clean, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("encode wanted report: %w", err)
	}
	return clean, nil
}

func sanitizeWantedSymbols(raw []string) []string {
	out := make([]string, 0, min(len(raw), maxWantedPackages))
	seen := map[string]bool{}
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 || seen[value] {
			continue
		}
		valid := true
		for _, r := range value {
			if unicode.IsControl(r) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if len(out) >= maxWantedPackages {
			break
		}
	}
	return out
}

func wantedEcosystem(ecosystem string) bool {
	switch ecosystem {
	case "npm", "pypi", "cargo", "golang", "gem", "composer", "hex", "pub", "maven", "generic":
		return true
	}
	return false
}
