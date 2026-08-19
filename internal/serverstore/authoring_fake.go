package serverstore

import (
	"context"
	"encoding/json"
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
				}
			}
		}
	}
	observedScores := make(map[[3]string]int64)
	packageScores := make(map[string]int64)
	for observed, score := range f.merge.observations {
		targetOS := ""
		if meta := f.aggMeta[observed]; meta != nil {
			targetOS = authoringEvidenceOS(meta.envJSON)
		}
		observedScores[[3]string{observed.PURL, observed.Symbol, targetOS}] += score
		packageScores[observed.PURL] += score
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
	for _, pkg := range f.packages {
		for targetOS := range packageTargets[pkg.PURL] {
			score := packageScores[pkg.PURL]
			if score == 0 || !eligible(pkg, "") {
				continue
			}
			key := candidateKey{pkg.Ecosystem, pkg.Name, pkg.Version, "", targetOS}
			if _, exists := candidates[key]; !exists {
				candidates[key] = WantedRow{Ecosystem: pkg.Ecosystem, Name: pkg.Name, Version: pkg.Version,
					Kind: "EXPANSION", Score: score, TargetOS: targetOS}
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
	for _, pkg := range f.packages {
		if verifiedPURLs[pkg.PURL] || !eligible(pkg, "") {
			continue
		}
		for targetOS := range nameTargets[[2]string{pkg.Ecosystem, pkg.Name}] {
			key := candidateKey{pkg.Ecosystem, pkg.Name, pkg.Version, "", targetOS}
			if _, exists := candidates[key]; !exists {
				candidates[key] = WantedRow{Ecosystem: pkg.Ecosystem, Name: pkg.Name,
					Version: pkg.Version, Kind: "EXPANSION", Score: 0, TargetOS: targetOS}
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
			}
		}
	}
	out := make([]WantedRow, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].TargetOS == "linux") != (out[j].TargetOS == "linux") {
			return out[i].TargetOS == "linux"
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "FINDING"
		}
		if out[i].Kind == "EXPANSION" && (out[i].Symbol == "") != (out[j].Symbol == "") {
			return out[i].Symbol == ""
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].Symbol < out[j].Symbol
	})
	// Depth is how many jobs this version has already been offered further up
	// the merit order. Ordering by it first means every version earns its
	// first job before any version earns its second, so the grid fills by
	// breadth across versions instead of deepening whichever version already
	// carries the most evidence. Merit still decides ties within a depth.
	type rankedCandidate struct {
		row   WantedRow
		depth int
	}
	ranked := make([]rankedCandidate, len(out))
	depths := make(map[[3]string]int, len(out))
	for i, row := range out {
		key := [3]string{row.Ecosystem, row.Name, row.Version}
		depths[key]++
		ranked[i] = rankedCandidate{row: row, depth: depths[key]}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if (ranked[i].row.TargetOS == "linux") != (ranked[j].row.TargetOS == "linux") {
			return ranked[i].row.TargetOS == "linux"
		}
		return ranked[i].depth < ranked[j].depth
	})
	for i, r := range ranked {
		out[i] = r.row
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
	for key, work := range f.authoringWork {
		if work.SampleID == "" && !now.Before(work.LeaseExpiresAt) {
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
