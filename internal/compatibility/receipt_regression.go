package compatibility

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// receiptTarget identifies the snapshot a receipt-established regression is
// attached to. A candidate belongs to the newer exact purl only.
type receiptTarget struct {
	purl   string
	symbol string
}

type receiptComparisonKey struct {
	ecosystem         string
	name              string
	caseID            string
	symbol            string
	contextLabel      string
	envBucketHash     string
	verifierAdapter   string
	sandboxCapability domain.SandboxCapability
	companions        string
	harnessHash       string
}

type receiptVersionVerdict struct {
	purl string
	pass int64
	fail int64
}

// regressionsFromReceipts finds exact, reproducible PASS -> FAIL boundaries.
// It is intentionally stricter than observation-based detection:
//
//   - both receipts are v2 and identify the exact resolved version;
//   - both resolve and compile stages passed;
//   - the signed case id is stable and agrees with the stored sample;
//   - symbol, bucketed environment, verifier adapter, sandbox capability and
//     every non-target resolved package are identical;
//   - the compared releases are adjacent among the measured releases; and
//   - each endpoint has an unambiguous contract verdict.
//
// Any unknown dimension yields no claim. In particular there is no manifest
// fallback: an author-declared version is not evidence of what ran.
func regressionsFromReceipts(samples []sampleData) map[receiptTarget][]RegressionCandidate {
	groups := map[receiptComparisonKey]map[string]*receiptVersionVerdict{}

	for _, sd := range samples {
		caseID := stableSampleCaseID(sd)
		if caseID == "" {
			continue
		}
		harnessHash := receiptHarnessHash(sd)
		if harnessHash == "" {
			continue
		}
		symbols := regressionSymbols(sd)
		for _, rec := range sd.receipts {
			if rec.CaseID == "" || rec.CaseID != caseID ||
				rec.Stages["resolve"] != string(domain.ResultPass) ||
				rec.Stages["compile"] != string(domain.ResultPass) ||
				(rec.Stages["contract"] != string(domain.ResultPass) &&
					rec.Stages["contract"] != string(domain.ResultFail)) ||
				len(rec.ResolvedPackages) == 0 ||
				rec.VerifierAdapter == "" || rec.SandboxCapability == "" {
				continue
			}
			bucket := rec.Env.Normalize().Bucketed()
			if bucket.SchemaVersion == 0 || bucket.Ecosystem == "" || bucket.OS == "" || bucket.Arch == "" {
				continue
			}

			// One verification cannot establish a one-variable boundary for an
			// identity that appears at two versions in the same resolver output.
			identityCount := map[pkgKey]int{}
			for _, p := range rec.ResolvedPackages {
				identityCount[pkgKey{p.Ecosystem, p.Name}]++
			}
			for _, p := range rec.ResolvedPackages {
				identity := pkgKey{p.Ecosystem, p.Name}
				if identityCount[identity] != 1 {
					continue
				}
				companions := receiptCompanions(rec.ResolvedPackages, identity)
				for _, symbol := range symbols {
					key := receiptComparisonKey{
						ecosystem: p.Ecosystem, name: p.Name, caseID: caseID,
						symbol: symbol, contextLabel: bucket.ContextLabel(),
						envBucketHash: bucket.Hash(), verifierAdapter: rec.VerifierAdapter,
						sandboxCapability: rec.SandboxCapability, companions: companions,
						harnessHash: harnessHash,
					}
					versions := groups[key]
					if versions == nil {
						versions = map[string]*receiptVersionVerdict{}
						groups[key] = versions
					}
					v := versions[p.Version]
					if v == nil {
						v = &receiptVersionVerdict{purl: p.String()}
						versions[p.Version] = v
					}
					if rec.Stages["contract"] == string(domain.ResultPass) {
						v.pass++
					} else {
						v.fail++
					}
				}
			}
		}
	}

	// Keep comparison dimensions on the public candidate. Collapsing them
	// let one adapter's PASS->FAIL survive even when a different adapter saw
	// the opposite boundary, while hiding which conditions established it.
	out := map[receiptTarget][]RegressionCandidate{}
	for key, byVersion := range groups {
		versions := make([]string, 0, len(byVersion))
		for version := range byVersion {
			versions = append(versions, version)
		}
		sort.Slice(versions, func(i, j int) bool {
			return domain.CompareVersions(versions[i], versions[j]) < 0
		})
		for i := 1; i < len(versions); i++ {
			prev, cur := byVersion[versions[i-1]], byVersion[versions[i]]
			if prev.pass == 0 || prev.fail != 0 || cur.fail == 0 || cur.pass != 0 {
				continue
			}
			candidate := RegressionCandidate{
				Package: cur.purl, PreviousPackage: prev.purl, CaseID: key.caseID,
				Symbol: key.symbol, Stage: string(domain.StageContract),
				VerifierAdapter: key.verifierAdapter, SandboxCapability: key.sandboxCapability,
				CompanionPackages: companionPackages(key.companions), HarnessHash: key.harnessHash,
				ContextLabel: key.contextLabel, EnvBucketHash: key.envBucketHash,
				FailRate: 1, PreviousPassRate: 1,
				Observations: cur.fail, PreviousObservations: prev.pass,
			}
			target := receiptTarget{purl: cur.purl, symbol: key.symbol}
			out[target] = append(out[target], candidate)
		}
	}

	for target := range out {
		sort.Slice(out[target], func(i, j int) bool {
			a, b := out[target][i], out[target][j]
			if a.PreviousPackage != b.PreviousPackage {
				return a.PreviousPackage < b.PreviousPackage
			}
			if a.CaseID != b.CaseID {
				return a.CaseID < b.CaseID
			}
			if a.EnvBucketHash != b.EnvBucketHash {
				return a.EnvBucketHash < b.EnvBucketHash
			}
			if a.VerifierAdapter != b.VerifierAdapter {
				return a.VerifierAdapter < b.VerifierAdapter
			}
			if a.SandboxCapability != b.SandboxCapability {
				return a.SandboxCapability < b.SandboxCapability
			}
			if a.HarnessHash != b.HarnessHash {
				return a.HarnessHash < b.HarnessHash
			}
			return strings.Join(a.CompanionPackages, "\x00") < strings.Join(b.CompanionPackages, "\x00")
		})
	}
	return out
}

// receiptHarnessHash compares the public, package-pin-independent harness
// description. CaseID already fixes the claims; these commands and adapter
// keep independently implemented samples from being treated as the same
// experiment merely because they solve the same problem.
func receiptHarnessHash(sd sampleData) string {
	doc := struct {
		BuildCommand    []string `json:"buildCommand,omitempty"`
		ContractCommand []string `json:"contractCommand"`
		VerifierAdapter string   `json:"verifierAdapter"`
	}{
		BuildCommand: sd.manifest.BuildCommand, ContractCommand: sd.manifest.ContractCommand,
		VerifierAdapter: sd.manifest.VerifierAdapter,
	}
	sum := sha256.Sum256(domain.MustCanonicalJSON(doc))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func companionPackages(key string) []string {
	if key == "" {
		return nil
	}
	return strings.Split(key, "\x00")
}

func stableSampleCaseID(sd sampleData) string {
	if sd.row.CaseID != "" {
		return sd.row.CaseID
	}
	if sd.manifest.Case.CaseID != "" {
		return sd.manifest.Case.CaseID
	}
	if sd.manifest.Case.SchemaVersion != 0 {
		return sd.manifest.Case.ComputeID()
	}
	return ""
}

func regressionSymbols(sd sampleData) []string {
	seen := map[string]bool{}
	var out []string
	for _, symbol := range sd.manifest.Symbols {
		if symbol != "" && !seen[symbol] {
			seen[symbol] = true
			out = append(out, symbol)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	sort.Strings(out)
	return out
}

func receiptCompanions(packages []domain.PURL, target pkgKey) string {
	var out []string
	for _, p := range packages {
		if p.Ecosystem == target.ecosystem && p.Name == target.name {
			continue
		}
		out = append(out, p.String())
	}
	sort.Strings(out)
	return strings.Join(out, "\x00")
}
