package compatibility

import (
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// JDKBoundaryCandidate is a receipt-established PASS/FAIL boundary where
// resolved packages and every non-JDK comparison dimension stayed fixed.
//
// Vendor is not a signed EnvironmentFingerprint field. Consequently this is
// deliberately a pinned-image-family candidate, not a claim that arbitrary
// installations of the named JDK vendor behave the same way.
type JDKBoundaryCandidate struct {
	Package              string                   `json:"package"`
	ResolvedPackages     []string                 `json:"resolvedPackages"`
	CaseID               string                   `json:"caseId"`
	Symbol               string                   `json:"symbol,omitempty"`
	Stage                string                   `json:"stage"`
	VerifierAdapter      string                   `json:"verifierAdapter"`
	SandboxCapability    domain.SandboxCapability `json:"sandboxCapability"`
	HarnessHash          string                   `json:"harnessHash"`
	LanguageVersion      string                   `json:"languageVersion"`
	LowerRuntimeVersion  string                   `json:"lowerRuntimeVersion"`
	HigherRuntimeVersion string                   `json:"higherRuntimeVersion"`
	LowerResult          string                   `json:"lowerResult"`
	HigherResult         string                   `json:"higherResult"`
	EnvironmentHash      string                   `json:"environmentHash"`
	LowerObservations    int64                    `json:"lowerObservations"`
	HigherObservations   int64                    `json:"higherObservations"`
}

type jdkComparisonKey struct {
	sampleID          string
	caseID            string
	symbol            string
	packages          string
	verifierAdapter   string
	sandboxCapability domain.SandboxCapability
	harnessHash       string
	languageVersion   string
	environmentHash   string
}

type jdkStageVerdict struct {
	pass int64
	fail int64
}

type jdkRuntimeVerdicts struct {
	compile  jdkStageVerdict
	contract jdkStageVerdict
}

var supportedJDKLines = map[string]bool{
	"8": true, "11": true, "17": true, "21": true, "25": true,
}

// jdkBoundariesFromReceipts detects compile or contract boundaries across the
// digest-pinned Java matrix. It does not share grouping or verdict state with
// package-version regression detection.
func jdkBoundariesFromReceipts(samples []sampleData) map[receiptTarget][]JDKBoundaryCandidate {
	groups := map[jdkComparisonKey]map[string]*jdkRuntimeVerdicts{}

	for _, sd := range samples {
		caseID := stableSampleCaseID(sd)
		harnessHash := receiptHarnessHash(sd)
		if caseID == "" || harnessHash == "" {
			continue
		}
		for _, rec := range sd.receipts {
			env, envHash, ok := comparableJDKEnvironment(sd.manifest, rec)
			if !ok || rec.CaseID == "" || rec.CaseID != caseID ||
				rec.Stages["resolve"] != string(domain.ResultPass) ||
				len(rec.ResolvedPackages) == 0 || rec.VerifierAdapter == "" ||
				rec.SandboxCapability == "" {
				continue
			}
			packages := exactPackageSet(rec.ResolvedPackages)
			for _, symbol := range regressionSymbols(sd) {
				key := jdkComparisonKey{
					sampleID: sd.row.SampleID, caseID: caseID, symbol: symbol, packages: packages,
					verifierAdapter:   rec.VerifierAdapter,
					sandboxCapability: rec.SandboxCapability,
					harnessHash:       harnessHash, languageVersion: env.LanguageVersion,
					environmentHash: envHash,
				}
				byRuntime := groups[key]
				if byRuntime == nil {
					byRuntime = map[string]*jdkRuntimeVerdicts{}
					groups[key] = byRuntime
				}
				verdicts := byRuntime[env.RuntimeVersion]
				if verdicts == nil {
					verdicts = &jdkRuntimeVerdicts{}
					byRuntime[env.RuntimeVersion] = verdicts
				}
				addJDKStageVerdict(&verdicts.compile, rec.Stages["compile"])
				if rec.Stages["compile"] == string(domain.ResultPass) {
					addJDKStageVerdict(&verdicts.contract, rec.Stages["contract"])
				}
			}
		}
	}

	out := map[receiptTarget][]JDKBoundaryCandidate{}
	for key, byRuntime := range groups {
		versions := make([]string, 0, len(byRuntime))
		for version := range byRuntime {
			versions = append(versions, version)
		}
		sort.Slice(versions, func(i, j int) bool {
			return domain.CompareVersions(versions[i], versions[j]) < 0
		})
		for i := 1; i < len(versions); i++ {
			lowerVersion, higherVersion := versions[i-1], versions[i]
			lower, higher := byRuntime[lowerVersion], byRuntime[higherVersion]
			for _, stage := range []struct {
				name         domain.Stage
				lower, upper jdkStageVerdict
			}{
				{name: domain.StageCompile, lower: lower.compile, upper: higher.compile},
				{name: domain.StageContract, lower: lower.contract, upper: higher.contract},
			} {
				lowerResult, lowerCount, lowerOK := unambiguousJDKVerdict(stage.lower)
				higherResult, higherCount, higherOK := unambiguousJDKVerdict(stage.upper)
				if !lowerOK || !higherOK || lowerResult == higherResult {
					continue
				}
				packages := strings.Split(key.packages, "\x00")
				for _, purl := range packages {
					candidate := JDKBoundaryCandidate{
						Package: purl, ResolvedPackages: append([]string(nil), packages...),
						CaseID: key.caseID, Symbol: key.symbol, Stage: string(stage.name),
						VerifierAdapter: key.verifierAdapter, SandboxCapability: key.sandboxCapability,
						HarnessHash: key.harnessHash, LanguageVersion: key.languageVersion,
						LowerRuntimeVersion: lowerVersion, HigherRuntimeVersion: higherVersion,
						LowerResult: lowerResult, HigherResult: higherResult,
						EnvironmentHash:   key.environmentHash,
						LowerObservations: lowerCount, HigherObservations: higherCount,
					}
					target := receiptTarget{purl: purl, symbol: key.symbol}
					out[target] = append(out[target], candidate)
				}
			}
		}
	}

	for target := range out {
		sort.Slice(out[target], func(i, j int) bool {
			a, b := out[target][i], out[target][j]
			if a.LowerRuntimeVersion != b.LowerRuntimeVersion {
				return domain.CompareVersions(a.LowerRuntimeVersion, b.LowerRuntimeVersion) < 0
			}
			if a.HigherRuntimeVersion != b.HigherRuntimeVersion {
				return domain.CompareVersions(a.HigherRuntimeVersion, b.HigherRuntimeVersion) < 0
			}
			if a.Stage != b.Stage {
				return a.Stage < b.Stage
			}
			if a.CaseID != b.CaseID {
				return a.CaseID < b.CaseID
			}
			return a.EnvironmentHash < b.EnvironmentHash
		})
	}
	return out
}

func comparableJDKEnvironment(manifest domain.SampleManifest, rec ReceiptInfo) (domain.EnvironmentFingerprint, string, bool) {
	env := rec.Env.Normalize()
	if !supportedJDKLines[env.RuntimeVersion] ||
		env.SchemaVersion == 0 || env.Ecosystem != "maven" || env.OS != "linux" ||
		env.OSVersionBucket != "2023" || env.Distro != "amzn" || env.Libc != "glibc" || env.Arch == "" ||
		env.Runtime != "java" || env.Language != "java" || env.ExecutionContext != "java" ||
		env.Compiler != "javac" || env.CompilerVersion != env.RuntimeVersion ||
		env.LanguageVersion == "" || env.PackageManager == "" || env.PackageManagerVersion == "" ||
		env.Virtualization != "container" || env.ContainerRuntime != "docker" ||
		rec.SandboxCapability != domain.CapContainerRun ||
		rec.VerifierAdapter != manifest.VerifierAdapter ||
		manifest.Environment.LanguageVersion != env.LanguageVersion {
		return domain.EnvironmentFingerprint{}, "", false
	}
	switch rec.VerifierAdapter {
	case "gradle-java@1":
		// gradle-java@1 generates the compiler configuration from the explicit
		// manifest language target; the authored build is not executed.
		wantGradle := "8.14.3"
		if env.RuntimeVersion == "25" {
			wantGradle = "9.7.0"
		}
		if env.PackageManager != "gradle" || env.PackageManagerVersion != wantGradle {
			return domain.EnvironmentFingerprint{}, "", false
		}
	case "maven-java@1":
		if env.PackageManager != "maven" || env.PackageManagerVersion != "3.9.11" ||
			!mavenCommandPinsLanguage(manifest.BuildCommand, env.LanguageVersion) {
			return domain.EnvironmentFingerprint{}, "", false
		}
	default:
		return domain.EnvironmentFingerprint{}, "", false
	}

	// RuntimeVersion and CompilerVersion are two signed representations of
	// the same pinned JDK line. Every other signed dimension remains in the
	// hash and therefore must match exactly.
	comparison := env
	comparison.RuntimeVersion = ""
	comparison.CompilerVersion = ""
	return env, comparison.Hash(), true
}

// mavenCommandPinsLanguage deliberately accepts only direct javac argument
// vectors. Shell strings, environment expansion and build-tool indirection are
// too ambiguous to prove which target bytecode was produced.
func mavenCommandPinsLanguage(command []string, target string) bool {
	if len(command) < 3 || command[0] != "javac" {
		return false
	}
	var releases, sources, targets []string
	for i := 1; i < len(command); i++ {
		if strings.HasPrefix(command[i], "@") {
			return false // an argfile can hide a conflicting compiler target
		}
		switch command[i] {
		case "--release", "-source", "-target":
			if i+1 >= len(command) {
				return false
			}
			value := command[i+1]
			switch command[i] {
			case "--release":
				releases = append(releases, value)
			case "-source":
				sources = append(sources, value)
			case "-target":
				targets = append(targets, value)
			}
			i++
		}
	}
	if len(releases) != 0 {
		return len(releases) == 1 && releases[0] == target && len(sources) == 0 && len(targets) == 0
	}
	if target != "8" || len(sources) != 1 || len(targets) != 1 {
		return false
	}
	return (sources[0] == "8" || sources[0] == "1.8") &&
		(targets[0] == "8" || targets[0] == "1.8")
}

func exactPackageSet(packages []domain.PURL) string {
	out := make([]string, len(packages))
	for i, p := range packages {
		out[i] = p.String()
	}
	sort.Strings(out)
	return strings.Join(out, "\x00")
}

func addJDKStageVerdict(verdict *jdkStageVerdict, result string) {
	switch result {
	case string(domain.ResultPass):
		verdict.pass++
	case string(domain.ResultFail):
		verdict.fail++
	}
}

func unambiguousJDKVerdict(verdict jdkStageVerdict) (string, int64, bool) {
	if verdict.pass > 0 && verdict.fail == 0 {
		return string(domain.ResultPass), verdict.pass, true
	}
	if verdict.fail > 0 && verdict.pass == 0 {
		return string(domain.ResultFail), verdict.fail, true
	}
	return "", 0, false
}
