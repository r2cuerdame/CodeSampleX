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
//
// The order is the one R2C-89 fixes: real demand, then the exact holes
// evidence already points at, then the dependency closure, then version
// breadth. Dependency work sits above siblings because a coordinate a
// lockfile actually resolved is something projects run, and a sibling release
// nobody has been seen using is not.
const (
	authoringRankFinding      = 0
	authoringRankPackageLevel = 1
	authoringRankSymbol       = 2
	authoringRankDependency   = 3
	authoringRankSibling      = 4
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
		// PostgreSQL restricts this branch to current clusters. A preserved
		// pre-0024 row is historical material, so ranking authoring work by
		// its observation count would spend the farm on a failure identity
		// the builder no longer writes.
		if !IsCurrentFailureCluster(cluster) {
			continue
		}
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
					Symbol: cluster.Symbol, Kind: "FINDING", Score: cluster.ObservationCount, TargetOS: targetOS,
					LastSeen: pkg.LastSeen}
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
	// observedPURLs is every coordinate evidence names at all, and chosenPURLs
	// the subset somebody listed in their own manifest. The dependency closure
	// needs both: it walks out of the chosen ones and stops at anything already
	// observed, because every other branch here can already reach those.
	observedPURLs := make(map[string]bool)
	chosenPURLs := make(map[string]bool)
	for observed, score := range f.merge.observations {
		targetOS := ""
		observedPURLs[observed.PURL] = true
		if meta := f.aggMeta[observed]; meta != nil {
			targetOS = authoringEvidenceOS(meta.envJSON)
			score *= authoringChoiceWeight(meta.direct)
			if meta.direct {
				chosenPURLs[observed.PURL] = true
			}
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
	// R2C-90: the resolved graph's own demand. How many distinct project-days
	// had this exact release resolved into them, which is the signal a carried
	// sighting count cannot carry -- see authoringResolveWeight.
	resolveDemand := f.resolveDemand()
	verifiedPURLs, packageTargets := f.provenCoordinates()
	// Package-level work is for an environment that has evidence but no proof
	// yet. Offering the pairs already proven — which is what this did — meant a
	// package proven on linux was offered for linux again, forever, and the
	// symbol filter never applies to a package-level row.
	for _, pkg := range f.packages {
		for targetOS, score := range observedTargets[pkg.PURL] {
			// A proof anywhere ends package-level coverage work. The OS-keyed
			// guard beside it asks a different question and cannot answer this
			// one: a work row's target_os is where the package was OBSERVED
			// (Windows, always) and the guard's is where the contract RAN
			// (Linux, always), so it compares linux against windows and never
			// matches.
			if score == 0 || packageTargets[pkg.PURL][targetOS] ||
				verifiedPURLs[pkg.PURL] || !eligible(pkg, "") {
				continue
			}
			key := candidateKey{pkg.Ecosystem, pkg.Name, pkg.Version, "", targetOS}
			if _, exists := candidates[key]; !exists {
				candidates[key] = WantedRow{Ecosystem: pkg.Ecosystem, Name: pkg.Name, Version: pkg.Version,
					Kind: "EXPANSION", Score: score + resolveDemand[pkg.PURL]*authoringResolveWeight,
					TargetOS: targetOS, LastSeen: pkg.LastSeen}
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
	nameTargets := f.provenNameTargets(packageTargets)
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
						Version: pkg.Version, Kind: "EXPANSION", Score: 0, TargetOS: targetOS,
						LastSeen: pkg.LastSeen}
					ranks[key] = authoringRankSibling
				}
			}
		}
	}
	for _, row := range f.dependencyClosure(observedPURLs, chosenPURLs, verifiedPURLs, nameTargets) {
		key := candidateKey{row.Ecosystem, row.Name, row.Version, "", ""}
		if _, exists := candidates[key]; !exists {
			candidates[key] = row
			ranks[key] = authoringRankDependency
		}
	}
	for _, pkg := range f.packages {
		for observed, score := range observedScores {
			// An observation with no symbol asks for package coverage, and
			// eligible() cannot answer that: it only compares symbol names, so
			// a symbol-less row was never excluded by anything and this branch
			// reissued an answered package forever — eight samples for
			// three@0.185.1 in twenty-eight minutes, each a placeholder goal
			// with no symbols.
			//
			// FINDING and WANTED rows are NOT filtered this way. A finding is
			// about a failure, not about coverage, and 77% of production's
			// clusters carry no symbol.
			if observed[0] != pkg.PURL || score == 0 || !eligible(pkg, observed[1]) {
				continue
			}
			if observed[1] == "" && verifiedPURLs[pkg.PURL] {
				continue
			}
			key := candidateKey{pkg.Ecosystem, pkg.Name, pkg.Version, observed[1], observed[2]}
			if existing, ok := candidates[key]; !ok || (existing.Kind != "FINDING" && score > existing.Score) {
				candidates[key] = WantedRow{Ecosystem: pkg.Ecosystem, Name: pkg.Name, Version: pkg.Version,
					Symbol: observed[1], Kind: "EXPANSION", Score: score, TargetOS: observed[2],
					LastSeen: pkg.LastSeen}
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
	// Coordinates an assignment already answered. ClaimAuthoringWork inserts
	// against a key nothing deletes once a sample is attached, so these can
	// never be handed out again -- and they sort by the observation count
	// that made them worth answering first, so they arrive at the TOP of a
	// finite window and push everything real out of it.
	//
	// Package-level EXPANSION and DEPENDENCY hand their row back on
	// submission and a symbol-bearing row is filtered by the verified-symbol
	// check above, so what actually accumulates here is the symbol-less
	// FINDING: 407 of them in production on 2026-08-23, which with 141 of
	// them inside a 200-row window left three claimable rows and took
	// authoring from 45 handouts an hour to zero for five hours.
	for key, candidate := range candidates {
		work, held := f.authoringWork[authoringWorkKey(candidate.Ecosystem,
			candidate.Name, candidate.Version, candidate.Symbol)]
		if held && work.SampleID != "" {
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
				// A live worker refreshing a hopeless claim used to hold the
				// slot until its 24-hour lease ran out. Reclaim released only
				// claims whose SESSION had died, and this one had not.
				if ledger := f.authoringAttempts[key]; ledger != nil && ledger.barred(sessionID, now) {
					delete(f.authoringWork, key)
					break
				}
				if ledger := f.authoringAttempts[key]; ledger == nil ||
					!now.Before(ledger.LastAttemptAt.Add(AuthoringAttemptDebounce)) {
					f.noteAuthoringHandout(key, work.Kind, sessionID, now)
				}
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
		if ledger := f.authoringAttempts[key]; ledger != nil && ledger.barred(sessionID, now) {
			continue
		}
		work := AuthoringWorkRow{Ecosystem: candidate.Ecosystem, Name: candidate.Name, Version: candidate.Version,
			Symbol: candidate.Symbol, Asks: candidate.Asks, Kind: candidate.Kind, Score: candidate.Score,
			SessionID: sessionID, ClaimedAt: now, LeaseExpiresAt: leaseExpiresAt}
		if work.Kind == "" {
			work.Kind = "WANTED"
		}
		f.authoringWork[key] = work
		f.noteAuthoringHandout(key, work.Kind, sessionID, now)
		return work, true, nil
	}
	return AuthoringWorkRow{}, false, nil
}

// noteAuthoringHandout opens an attempt against a coordinate. Caller holds f.mu.
func (f *Fake) noteAuthoringHandout(key [4]string, kind, sessionID string, now time.Time) {
	ledger := f.authoringAttempts[key]
	if ledger == nil {
		ledger = newAuthoringLedger(key[0], key[1], key[2], key[3])
		f.authoringAttempts[key] = ledger
	}
	ledger.handout(kind, sessionID, now)
}

func (f *Fake) ReportAuthoringOutcome(_ context.Context, sessionID string, outcome AuthoringOutcome, detail string, now time.Time) (AuthoringWorkRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, work := range f.authoringWork {
		if work.SessionID != sessionID || work.SampleID != "" || !now.Before(work.LeaseExpiresAt) {
			continue
		}
		ledger := f.authoringAttempts[key]
		if ledger == nil {
			ledger = newAuthoringLedger(key[0], key[1], key[2], key[3])
			f.authoringAttempts[key] = ledger
		}
		ledger.report(sessionID, outcome, detail, now)
		// The claim goes back immediately. A writer that has said what it
		// found should not also have to sit on the lease.
		delete(f.authoringWork, key)
		return work, true, nil
	}
	return AuthoringWorkRow{}, false, nil
}

func (f *Fake) ListAuthoringQuarantine(_ context.Context, now time.Time, limit int) ([]AuthoringAttemptState, error) {
	if limit < 1 {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AuthoringAttemptState, 0, len(f.authoringAttempts))
	for _, ledger := range f.authoringAttempts {
		if ledger.Withheld(now) {
			out = append(out, ledger.state())
		}
	}
	sortAuthoringQuarantine(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *Fake) AuthoringAttemptState(_ context.Context, ecosystem, name, version, symbol string) (AuthoringAttemptState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ledger := f.authoringAttempts[authoringWorkKey(ecosystem, name, version, symbol)]
	if ledger == nil {
		return AuthoringAttemptState{}, false, nil
	}
	return ledger.state(), true, nil
}

func (f *Fake) ReopenAuthoringQuarantine(_ context.Context, ecosystem, name, version, symbol string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ledger := f.authoringAttempts[authoringWorkKey(ecosystem, name, version, symbol)]
	if ledger == nil {
		return false, nil
	}
	return ledger.reopen(now), nil
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
	if ledger := f.authoringAttempts[key]; ledger != nil {
		ledger.authored(sessionID, now)
	}
	// A package-level claim is keyed (ecosystem,name,version,'') whichever
	// symbol the writer ended up choosing, so leaving the row behind with a
	// sample id would take that coordinate off the board permanently. A
	// DEPENDENCY claim has the same shape and the same reason: it asks about
	// the release, not about one symbol in it.
	if (current.Kind == "EXPANSION" || current.Kind == "DEPENDENCY") && current.Symbol == "" {
		delete(f.authoringWork, key)
		return true, nil
	}
	current.SampleID = sampleID
	f.authoringWork[key] = current
	return true, nil
}

// authoringChoiceWeight is how much more a chosen sighting counts than a
// carried one when ranking authoring work.
//
// Raw volume ranked the shadow of popular libraries: a transitive dependency
// pulled into a thousand lockfiles beat a package fifty developers listed
// themselves, and the queue then wrote samples for the shadow. The ratio is
// the distance between "somebody wanted this" and "somebody received this".
func authoringChoiceWeight(direct bool) int64 {
	if direct {
		return authoringDirectWeight
	}
	return 1
}

// resolveDemand is the distinct project-days that resolved each release, read
// from the resolved graph rather than from anybody's manifest. The PG half is
// the resolve_demand CTE; the two are compared row for row by
// TestIntegrationAuthoringExpansionFakeMatchesPostgres. Caller holds f.mu.
func (f *Fake) resolveDemand() map[string]int64 {
	days := make(map[string]map[string]bool)
	for edge, projectDays := range f.edges {
		child := domain.PURL{Ecosystem: edge.ecosystem, Name: edge.childName,
			Version: edge.childVersion}.String()
		if days[child] == nil {
			days[child] = make(map[string]bool)
		}
		for day := range projectDays {
			days[child][day] = true
		}
	}
	out := make(map[string]int64, len(days))
	for child, seen := range days {
		out[child] = int64(len(seen))
	}
	return out
}
