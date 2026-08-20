package serverstore

import (
	"context"
	"encoding/json"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"sort"
	"strings"
	"time"
)

func (f *Fake) IssueAuthoringSessions(_ context.Context, rows []AuthoringSessionRow, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	active := 0
	for _, row := range f.authoring {
		if row.RevokedAt.IsZero() && now.Before(row.IdleExpiresAt) {
			active++
		}
	}
	if active+len(rows) > MaxAuthoringSessions {
		return ErrAuthoringSessionLimit
	}
	seenTokens := make(map[string]struct{}, len(rows))
	seenSessions := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, exists := f.authoring[row.TokenHash]; exists {
			return ErrAuthoringSessionMissing
		}
		if _, exists := seenTokens[row.TokenHash]; exists {
			return ErrAuthoringSessionMissing
		}
		for _, existing := range f.authoring {
			if existing.SessionID == row.SessionID {
				return ErrAuthoringSessionMissing
			}
		}
		if _, exists := seenSessions[row.SessionID]; exists {
			return ErrAuthoringSessionMissing
		}
		seenTokens[row.TokenHash] = struct{}{}
		seenSessions[row.SessionID] = struct{}{}
	}
	for _, row := range rows {
		f.authoring[row.TokenHash] = row
	}
	return nil
}

func (f *Fake) RotateAuthoringSession(_ context.Context, sessionID, tokenHash string, now, idleExpiresAt time.Time) (AuthoringSessionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.authoring[tokenHash]; exists {
		return AuthoringSessionRow{}, ErrAuthoringSessionMissing
	}
	for oldHash, row := range f.authoring {
		if row.SessionID != sessionID || !row.RevokedAt.IsZero() || !now.Before(row.IdleExpiresAt) {
			continue
		}
		delete(f.authoring, oldHash)
		row.TokenHash = tokenHash
		row.LastRefreshAt = time.Time{}
		row.IdleExpiresAt = idleExpiresAt
		row.LastRefreshIP = ""
		row.ComputerName = ""
		f.authoring[tokenHash] = row
		return row, nil
	}
	return AuthoringSessionRow{}, ErrAuthoringSessionMissing
}

func (f *Fake) RefreshAuthoringSession(_ context.Context, tokenHash, ip, computerName string, now, idleExpiresAt time.Time) (AuthoringSessionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.authoring[tokenHash]
	if !ok || !row.RevokedAt.IsZero() {
		return AuthoringSessionRow{}, ErrAuthoringSessionMissing
	}
	if !now.Before(row.IdleExpiresAt) {
		return AuthoringSessionRow{}, ErrAuthoringSessionExpired
	}
	if row.LastRefreshAt.IsZero() || !row.LastRefreshAt.After(now.Add(-5*time.Minute)) {
		row.LastRefreshAt = now
		row.IdleExpiresAt = idleExpiresAt
		if ip != "" {
			row.LastRefreshIP = ip
		}
		if computerName != "" {
			row.ComputerName = computerName
		}
		f.authoring[tokenHash] = row
	}
	return row, nil
}

func (f *Fake) RevokeAuthoringSession(_ context.Context, sessionID string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, row := range f.authoring {
		if row.SessionID == sessionID && row.RevokedAt.IsZero() {
			row.RevokedAt = now
			f.authoring[key] = row
			// Hand back whatever it was holding. The lease runs 24 hours and
			// the assignment key does not record who holds it, so a claim left
			// behind takes its coordinates off the board for every other worker
			// for a day. Work already submitted keeps its row: it has a sample.
			for akey, work := range f.authoringWork {
				if work.SessionID == sessionID && work.SampleID == "" {
					delete(f.authoringWork, akey)
				}
			}
			return true, nil
		}
	}
	return false, nil
}

func (f *Fake) ListAuthoringSessions(_ context.Context, now time.Time, limit int) ([]AuthoringSessionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AuthoringSessionRow
	for _, row := range f.authoring {
		if row.RevokedAt.IsZero() && now.Before(row.IdleExpiresAt) {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IssuedAt.Equal(out[j].IssuedAt) {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].IssuedAt.After(out[j].IssuedAt)
	})
	if limit < 1 || limit > MaxAuthoringSessions {
		limit = MaxAuthoringSessions
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

var _ AuthoringSessionStore = (*Fake)(nil)

// Source ranks mirror the branch order of the PostgreSQL expansion query.
const (
	authoringRankFinding      = 0
	authoringRankPackageLevel = 1
	authoringRankSymbol       = 2
	authoringRankSibling      = 3
)

func (f *Fake) ListAuthoringExpansionCandidates(_ context.Context, limit int) ([]WantedRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit < 1 {
		return nil, nil
	}
	if limit > 200 {
		limit = 200
	}
	type candidateKey struct{ ecosystem, name, version, symbol, targetOS string }
	candidates := make(map[candidateKey]WantedRow)
	// ranks mirror the PG query's branch order (source_rank there):
	// FINDING 0, package-level expansion 1, symbol expansion 2, sibling 3.
	// The Fake used to proxy this with "empty symbol sorts first", which
	// promoted rank-3 siblings above scored symbol work and made the two
	// stores dispatch different jobs for the same data.
	ranks := make(map[candidateKey]int)
	eligible := func(pkg PackageRow, symbol string) bool {
		if pkg.Version == "" || pkg.Publicness != "PUBLIC" {
			return false
		}
		for sampleID, sample := range f.samples {
			if sample.Quarantined {
				continue
			}
			hasPass := false
			for _, receipt := range f.receipts[sampleID] {
				if receipt.ContractResult == "PASS" {
					hasPass = true
					break
				}
			}
			if !hasPass {
				continue
			}
			var manifest struct {
				Packages []string `json:"packages"`
				Symbols  []string `json:"symbols"`
			}
			if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) != nil || !containsString(manifest.Packages, pkg.PURL) {
				continue
			}
			if symbol != "" && containsString(manifest.Symbols, symbol) {
				return false
			}
		}
		return true
	}
	for _, cluster := range f.clusters {
		var versions []string
		if json.Unmarshal([]byte(cluster.VersionsJSON), &versions) != nil {
			continue
		}
		for _, version := range versions {
			for _, pkg := range f.packages {
				if pkg.Ecosystem != cluster.Ecosystem || pkg.Name != cluster.PackageName || pkg.Version != version || !eligible(pkg, cluster.Symbol) {
					continue
				}
				targetOS := authoringEvidenceOS(cluster.EnvSummaryJSON)
				if targetOS == "" {
					targetOS = f.authoringObservationOS(pkg.PURL, cluster.Symbol, cluster.ErrorFingerprint)
				}
				key := candidateKey{pkg.Ecosystem, pkg.Name, pkg.Version, cluster.Symbol, targetOS}
				candidate := WantedRow{Ecosystem: pkg.Ecosystem, Name: pkg.Name, Version: pkg.Version,
					Symbol: cluster.Symbol, Kind: "FINDING", Score: cluster.ObservationCount, TargetOS: targetOS}
				if old, ok := candidates[key]; !ok || candidate.Score > old.Score {
					candidates[key] = candidate
					ranks[key] = authoringRankFinding
				}
			}
		}
	}
	observedScores := make(map[[3]string]int64)
	packageScores := make(map[string]int64)
	// (purl, os) pairs evidence has actually seen, with their weight.
	observedTargets := make(map[string]map[string]int64)
	for observed, score := range f.merge.observations {
		targetOS := ""
		if meta := f.aggMeta[observed]; meta != nil {
			targetOS = authoringEvidenceOS(meta.envJSON)
		}
		observedScores[[3]string{observed.PURL, observed.Symbol, targetOS}] += score
		packageScores[observed.PURL] += score
		if targetOS != "" {
			if observedTargets[observed.PURL] == nil {
				observedTargets[observed.PURL] = map[string]int64{}
			}
			observedTargets[observed.PURL][targetOS] += score
		}
	}
	packageTargets := make(map[string]map[string]bool)
	verifiedPURLs := make(map[string]bool)
	for sampleID, sample := range f.samples {
		if sample.Quarantined {
			continue
		}
		var manifest struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) != nil {
			continue
		}
		for _, receipt := range f.receipts[sampleID] {
			if receipt.ContractResult != "PASS" {
				continue
			}
			for _, purl := range manifest.Packages {
				verifiedPURLs[purl] = true
			}
			var parsed struct {
				Environment struct {
					OS string `json:"os"`
				} `json:"environment"`
			}
			if json.Unmarshal([]byte(receipt.ReceiptJSON), &parsed) != nil || parsed.Environment.OS == "" {
				continue
			}
			for _, purl := range manifest.Packages {
				if packageTargets[purl] == nil {
					packageTargets[purl] = make(map[string]bool)
				}
				packageTargets[purl][strings.ToLower(parsed.Environment.OS)] = true
			}
		}
	}
	// Package-level work is for an environment that has evidence but no proof
	// yet. Offering the pairs already proven — which is what this did — meant a
	// package proven on linux was offered for linux again, forever, and the
	// symbol filter never applies to a package-level row.
	for _, pkg := range f.packages {
		for targetOS, score := range observedTargets[pkg.PURL] {
			if score == 0 || packageTargets[pkg.PURL][targetOS] || !eligible(pkg, "") {
				continue
			}
			key := candidateKey{pkg.Ecosystem, pkg.Name, pkg.Version, "", targetOS}
			if _, exists := candidates[key]; !exists {
				candidates[key] = WantedRow{Ecosystem: pkg.Ecosystem, Name: pkg.Name, Version: pkg.Version,
					Kind: "EXPANSION", Score: score, TargetOS: targetOS}
				ranks[key] = authoringRankPackageLevel
			}
		}
	}
	// Sibling versions of a package already proven at some version, but
	// carrying no verified sample of their own. Every other branch reaches a
	// version only through an evidence row keyed by the exact purl, so a
	// release nobody has measured can never become work -- and its column in
	// the matrix stays blank however long the workers run. Score 0 puts these
	// last on merit; version_depth is what actually lifts them into reach.
	nameTargets := make(map[[2]string]map[string]bool)
	for purl, oses := range packageTargets {
		proven, ok := f.packages[purl]
		if !ok {
			continue
		}
		key := [2]string{proven.Ecosystem, proven.Name}
		if nameTargets[key] == nil {
			nameTargets[key] = make(map[string]bool)
		}
		for targetOS := range oses {
			nameTargets[key][targetOS] = true
		}
	}
	// Only the newest few siblings per package. Uncapped, one long release
	// history fills the whole window with score-0 rows -- see
	// authoringSiblingVersionsPerPackage. The ordering (last_seen, then version
	// descending as a STRING) is deliberately the one PostgreSQL can express in
	// the same query: it is a safety cap, not a ranking, and a cap that the two
	// stores disagree about is worse than a cap that picks an imperfect six.
	siblingsByName := make(map[[2]string][]PackageRow)
	for _, pkg := range f.packages {
		if verifiedPURLs[pkg.PURL] || !eligible(pkg, "") {
			continue
		}
		name := [2]string{pkg.Ecosystem, pkg.Name}
		if len(nameTargets[name]) == 0 {
			continue
		}
		siblingsByName[name] = append(siblingsByName[name], pkg)
	}
	for name, pkgs := range siblingsByName {
		sort.Slice(pkgs, func(i, j int) bool {
			if !pkgs[i].LastSeen.Equal(pkgs[j].LastSeen) {
				return pkgs[i].LastSeen.After(pkgs[j].LastSeen)
			}
			return pkgs[i].Version > pkgs[j].Version
		})
		if len(pkgs) > authoringSiblingVersionsPerPackage {
			pkgs = pkgs[:authoringSiblingVersionsPerPackage]
		}
		for _, pkg := range pkgs {
			for targetOS := range nameTargets[name] {
				key := candidateKey{pkg.Ecosystem, pkg.Name, pkg.Version, "", targetOS}
				if _, exists := candidates[key]; !exists {
					candidates[key] = WantedRow{Ecosystem: pkg.Ecosystem, Name: pkg.Name,
						Version: pkg.Version, Kind: "EXPANSION", Score: 0, TargetOS: targetOS}
					ranks[key] = authoringRankSibling
				}
			}
		}
	}
	for _, pkg := range f.packages {
		for observed, score := range observedScores {
			if observed[0] != pkg.PURL || score == 0 || !eligible(pkg, observed[1]) {
				continue
			}
			key := candidateKey{pkg.Ecosystem, pkg.Name, pkg.Version, observed[1], observed[2]}
			if existing, ok := candidates[key]; !ok || (existing.Kind != "FINDING" && score > existing.Score) {
				candidates[key] = WantedRow{Ecosystem: pkg.Ecosystem, Name: pkg.Name, Version: pkg.Version,
					Symbol: observed[1], Kind: "EXPANSION", Score: score, TargetOS: observed[2]}
				ranks[key] = authoringRankSymbol
			}
		}
	}
	// Mirror the PG query exactly. Two orders matter and they are different:
	// depth is counted in the window's ORDER BY, which has no OS term, and only
	// the final ordering puts linux first. Counting depth in the OS-first order
	// let a windows row take depth 1 from a linux row of the same version.
	type rankedRow struct {
		row   WantedRow
		rank  int
		depth int
	}
	// Coordinates a worker has already answered and is waiting on an
	// independent verification for. Nothing about "proven" changes until that
	// verification lands, so without this the coordinate stayed a candidate and
	// the next worker claimed it minutes after the first submitted.
	inFlight := make(map[[2]string]bool)
	for _, draft := range f.authoringDrafts {
		sample, ok := f.samples[draft.SampleID]
		if !ok || sample.Status != "DRAFT" {
			continue
		}
		var manifest struct {
			Packages []string `json:"packages"`
			Symbols  []string `json:"symbols"`
		}
		if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) != nil {
			continue
		}
		for _, purl := range manifest.Packages {
			inFlight[[2]string{purl, ""}] = true
			for _, symbol := range manifest.Symbols {
				inFlight[[2]string{purl, symbol}] = true
			}
		}
	}
	for key, candidate := range candidates {
		purl := domain.PURL{Ecosystem: candidate.Ecosystem, Name: candidate.Name, Version: candidate.Version}.String()
		if inFlight[[2]string{purl, candidate.Symbol}] {
			delete(candidates, key)
			delete(ranks, key)
		}
	}

	ranked := make([]rankedRow, 0, len(candidates))
	for key, candidate := range candidates {
		ranked = append(ranked, rankedRow{row: candidate, rank: ranks[key]})
	}
	// PG: ROW_NUMBER() OVER (PARTITION BY ecosystem,name,version
	//                        ORDER BY source_rank,score DESC,last_seen DESC,symbol)
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.row.Score != b.row.Score {
			return a.row.Score > b.row.Score
		}
		if !a.row.LastSeen.Equal(b.row.LastSeen) {
			return a.row.LastSeen.After(b.row.LastSeen)
		}
		if a.row.Ecosystem != b.row.Ecosystem {
			return a.row.Ecosystem < b.row.Ecosystem
		}
		if a.row.Name != b.row.Name {
			return a.row.Name < b.row.Name
		}
		if a.row.Version != b.row.Version {
			return a.row.Version < b.row.Version
		}
		return a.row.Symbol < b.row.Symbol
	})
	depths := make(map[[3]string]int, len(ranked))
	for i := range ranked {
		key := [3]string{ranked[i].row.Ecosystem, ranked[i].row.Name, ranked[i].row.Version}
		depths[key]++
		ranked[i].depth = depths[key]
	}
	// PG: ORDER BY version_depth,score DESC,source_rank,
	//              last_seen DESC,ecosystem,name,version,symbol
	//
	// The linux-first term that used to lead this is gone. It was arbitrary
	// when written and became actively wrong: every observation this network
	// holds is recorded on Windows, so preferring Linux pushed the entire
	// measured demand behind work nobody asked for.
	//
	// Depth still leads, because it is what stops one package with a long
	// release history filling the whole window. Within a depth, the most-used
	// coordinate wins -- authoring should follow what people actually run.
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.depth != b.depth {
			return a.depth < b.depth
		}
		return a.row.Score > b.row.Score
	})
	out := make([]WantedRow, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.row)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func authoringEvidenceOS(raw string) string {
	var env struct {
		OS string `json:"os"`
	}
	if json.Unmarshal([]byte(raw), &env) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(env.OS))
}

func (f *Fake) authoringObservationOS(purl, symbol, errorFingerprint string) string {
	found := ""
	for key, meta := range f.aggMeta {
		if key.PURL != purl || key.Symbol != symbol || key.ErrorFP != errorFingerprint || meta == nil {
			continue
		}
		current := authoringEvidenceOS(meta.envJSON)
		if found != "" && current != found {
			return ""
		}
		found = current
	}
	return found
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (f *Fake) SaveAuthoringDraft(_ context.Context, row AuthoringDraftRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.authoringDrafts[row.SampleID]; ok && !existing.CreatedAt.IsZero() {
		row.CreatedAt = existing.CreatedAt
	}
	f.authoringDrafts[row.SampleID] = row
	return nil
}

func (f *Fake) ListAuthoringDrafts(_ context.Context, limit int) ([]AuthoringDraftRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AuthoringDraftRow, 0, len(f.authoringDrafts))
	for _, row := range f.authoringDrafts {
		row.VerificationStatus = "PENDING"
		if sample, ok := f.samples[row.SampleID]; ok && sample.Status == "CROSS_PASS" && !sample.Quarantined {
			row.VerificationStatus = "CROSS_PASS"
		} else {
			for _, receipt := range f.receipts[row.SampleID] {
				if receipt.ContractResult == "FAIL" {
					row.VerificationStatus = "CROSS_FAIL"
				}
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].SampleID < out[j].SampleID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit < 1 || limit > 100 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func authoringWorkKey(ecosystem, name, version, symbol string) [4]string {
	return [4]string{ecosystem, name, version, symbol}
}

func (f *Fake) ClaimAuthoringWork(_ context.Context, sessionID string, candidates []WantedRow, now, leaseExpiresAt time.Time) (AuthoringWorkRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	eligible := make(map[[4]string]struct{}, len(candidates))
	for _, candidate := range candidates {
		eligible[authoringWorkKey(candidate.Ecosystem, candidate.Name, candidate.Version, candidate.Symbol)] = struct{}{}
	}
	sessionLive := func(id string) bool {
		for _, row := range f.authoring {
			if row.SessionID == id {
				return row.RevokedAt.IsZero() && now.Before(row.IdleExpiresAt)
			}
		}
		// A session the store never saw cannot be judged dead; leave its claim.
		return true
	}
	for key, work := range f.authoringWork {
		if work.SampleID == "" && !now.Before(work.LeaseExpiresAt) {
			delete(f.authoringWork, key)
			continue
		}
		// Revoking is not the only way a session stops. One that simply quits
		// refreshing idles out in an hour while its claim runs for a day, and
		// the assignment key does not record who holds it — so the coordinate
		// is off the board for everybody until the lease expires.
		if work.SampleID == "" && !sessionLive(work.SessionID) {
			delete(f.authoringWork, key)
			continue
		}
		if work.SessionID == sessionID && work.SampleID == "" && now.Before(work.LeaseExpiresAt) {
			if _, stillEligible := eligible[key]; stillEligible {
				return work, true, nil
			}
			// Candidate selection changed (for example an environment was
			// discovered to be unsupported). Release the untouched lease so
			// this session can immediately receive compatible work.
			delete(f.authoringWork, key)
		}
	}
	for _, candidate := range candidates {
		key := authoringWorkKey(candidate.Ecosystem, candidate.Name, candidate.Version, candidate.Symbol)
		if _, exists := f.authoringWork[key]; exists {
			continue
		}
		work := AuthoringWorkRow{Ecosystem: candidate.Ecosystem, Name: candidate.Name, Version: candidate.Version,
			Symbol: candidate.Symbol, Asks: candidate.Asks, Kind: candidate.Kind, Score: candidate.Score,
			SessionID: sessionID, ClaimedAt: now, LeaseExpiresAt: leaseExpiresAt}
		if work.Kind == "" {
			work.Kind = "WANTED"
		}
		f.authoringWork[key] = work
		return work, true, nil
	}
	return AuthoringWorkRow{}, false, nil
}

func (f *Fake) AuthoringWorkForSubmission(_ context.Context, sessionID, sampleID string, now time.Time) (AuthoringWorkRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, work := range f.authoringWork {
		if work.SampleID == "" && !now.Before(work.LeaseExpiresAt) {
			delete(f.authoringWork, key)
			continue
		}
		if work.SessionID == sessionID && ((work.SampleID == "" && now.Before(work.LeaseExpiresAt)) || work.SampleID == sampleID) {
			return work, true, nil
		}
	}
	return AuthoringWorkRow{}, false, nil
}

func (f *Fake) AttachAuthoringWorkSample(_ context.Context, sessionID string, work AuthoringWorkRow, sampleID string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := authoringWorkKey(work.Ecosystem, work.Name, work.Version, work.Symbol)
	current, ok := f.authoringWork[key]
	if !ok || current.SessionID != sessionID || current.SampleID != "" || !now.Before(current.LeaseExpiresAt) {
		return false, nil
	}
	if current.Kind == "EXPANSION" && current.Symbol == "" {
		delete(f.authoringWork, key)
		return true, nil
	}
	current.SampleID = sampleID
	f.authoringWork[key] = current
	return true, nil
}
