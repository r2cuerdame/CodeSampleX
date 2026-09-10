package evidence

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	csxenv "github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// usageFacts is what one scan learned about one package without building it.
type usageFacts struct {
	Epoch, PURL, EnvHash  string
	Direct                bool
	Coresident, DependsOn []string
}

// usageObsKey builds the USED observation for one package.
func usageObsKey(f usageFacts) localdb.ObsKey {
	return localdb.ObsKey{
		Epoch:      f.Epoch,
		PURL:       f.PURL,
		EnvHash:    f.EnvHash,
		Stage:      domain.StageUsed,
		Result:     domain.ResultPass,
		Direct:     f.Direct,
		Coresident: f.Coresident,
		DependsOn:  f.DependsOn,
	}
}

// publicPURLs is the public subset of a scan as a slice, for co-residence.
func publicPURLs(public map[string]domain.PURL) []domain.PURL {
	out := make([]domain.PURL, 0, len(public))
	for _, p := range public {
		out = append(out, p)
	}
	return out
}

// Recorder turns one wrapped command run into local aggregate
// observations (contract C14 steps 4–5). Only PUBLIC packages ever reach
// the observations table; PRIVATE and UNKNOWN packages are recorded in
// the local packages inventory only, which is never uploaded.
type Recorder struct {
	DB    *localdb.DB
	Ident *identity.Identity
	Cfg   *config.Config
}

// RecordRun records the outcome of one `csx run` execution.
//
//   - Every scanned package is upserted into the local packages table
//     (local diagnostics; never uploaded).
//   - Known commands record, per PUBLIC package, one package-level
//     observation (symbol "") and one observation per public symbol usage,
//     at the classified stage with PASS/FAIL from exitCode. On FAIL the
//     failure tail — the stream that carried the diagnosis, see
//     CommandOutput.FailureDiagnostics — is sanitized (stage-aware) and
//     only the resulting fingerprint + error code are attached, never the
//     raw text.
//   - Unknown commands (profile.Known == false) prove nothing beyond
//     "these public packages are in use": they record only USED/PASS
//     package rows.
//   - Symbol sightings are stored with the rotating project bucket
//     derived from the absolute project path (HMAC, irreversible).
func (r *Recorder) RecordRun(ctx context.Context, dir string, res *scanner.ScanResult, profile scanner.CommandProfile, exitCode int, failureTail string) error {
	if exitCode == 0 {
		return r.recordRun(ctx, dir, res, profile, false, domain.FailureTermination{}, failureTail, nil)
	}
	code := exitCode
	return r.recordRun(ctx, dir, res, profile, true,
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}, failureTail, nil)
}

// RecordTerminatedRun records a caller-observed structured termination,
// including exit, timeout, signal, and process-start failure. Callers pass the
// structured state; this method never guesses one from missing fields.
func (r *Recorder) RecordTerminatedRun(ctx context.Context, dir string, res *scanner.ScanResult,
	profile scanner.CommandProfile, term domain.FailureTermination, failureTail string) error {
	return r.recordRun(ctx, dir, res, profile, true, term, failureTail, nil)
}

type classifiedFailure struct {
	stage    domain.Stage
	evidence domain.FailureEvidence
}

// RecordCommandOutput analyzes the actual failure markers before recording.
// One outer execution may therefore append multiple independent failure rows.
func (r *Recorder) RecordCommandOutput(ctx context.Context, dir string, res *scanner.ScanResult,
	profile scanner.CommandProfile, argv []string, exitCode int, output CommandOutput) error {
	var recordErr error
	if exitCode == 0 && output.Termination.Kind == "" {
		recordErr = r.recordRun(ctx, dir, res, profile, false, domain.FailureTermination{}, "", nil)
	} else {
		analysis := AnalyzeFailure(profile, argv, output)
		failures := make([]classifiedFailure, 0, len(analysis.Events))
		for _, event := range analysis.Events {
			failure := sanitizer.SanitizeClassifiedFailure(event.Diagnostic, event.Stage, output.Termination, nil,
				analysis.OuterCommand, analysis.OuterStage, event.Toolchain, event.StageEvidence, event.EvidenceGap)
			failures = append(failures, classifiedFailure{stage: event.Stage, evidence: failure})
		}
		recordErr = r.recordRun(ctx, dir, res, profile, true, output.Termination, "", failures)
	}

	if r.DB != nil && r.Cfg != nil && r.Cfg.Mode == config.ModeCommunity && len(argv) > 0 {
		tool := domain.CommandTool(argv)
		if domain.IsRecognizedCLITool(tool) {
			coord := domain.ParseCLICommand(argv, cliEnvironment(res))
			coord.ToolVersion = output.ToolVersion
			coord.Shell = output.Shell
			startedAt := output.StartedAt.UTC().Format(time.RFC3339Nano)
			finishedAt := output.FinishedAt.UTC().Format(time.RFC3339Nano)
			if output.StartedAt.IsZero() {
				startedAt = ""
			}
			if output.FinishedAt.IsZero() {
				finishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			stdout := sanitizedStreamEvidence(output.Stdout, output.StdoutTruncated)
			stderr := sanitizedStreamEvidence(output.Stderr, output.StderrTruncated)
			quality := cliEvidenceQuality(coord, startedAt, finishedAt, output.Termination, exitCode)
			if exitCode == 0 && output.Termination.Kind == "" {
				code := 0
				_ = r.DB.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
					Coordinate: coord,
					Provenance: domain.ProvenanceField,
					Result:     domain.ResultPass,
					Termination: domain.FailureTermination{
						Kind:     domain.TerminationExit,
						ExitCode: &code,
					},
					EvidenceQuality: quality,
					ObservedAt:      finishedAt,
					StartedAt:       startedAt,
					FinishedAt:      finishedAt,
					EnvironmentID:   coord.Environment.Hash(),
					Stdout:          stdout,
					Stderr:          stderr,
					Count:           1,
				})
			} else if !profile.Known || profile.Stage == domain.StageProjectProcess {
				term := output.Termination
				if term.Kind == "" {
					code := exitCode
					term = domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}
				}
				analysis := AnalyzeFailure(profile, argv, output)
				var errorFP, errorCode, errorSummary string
				if len(analysis.Events) > 0 {
					ev := sanitizer.SanitizeClassifiedFailure(analysis.Events[0].Diagnostic, analysis.Events[0].Stage,
						output.Termination, nil, analysis.OuterCommand, analysis.OuterStage,
						analysis.Events[0].Toolchain, analysis.Events[0].StageEvidence, analysis.Events[0].EvidenceGap)
					errorFP = ev.Fingerprint
					errorCode = ev.ErrorCode
					errorSummary = ev.ErrorSummary
				} else {
					diag := output.FailureDiagnostics()
					if diag == "" {
						diag = output.Stderr
					}
					ev := sanitizer.SanitizeFailure(diag, domain.StageProjectProcess, term, nil)
					errorFP = ev.Fingerprint
					errorCode = ev.ErrorCode
					errorSummary = ev.ErrorSummary
				}
				_ = r.DB.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
					Coordinate:        coord,
					Provenance:        domain.ProvenanceField,
					Result:            domain.ResultFail,
					Termination:       term,
					ErrorFingerprint:  errorFP,
					ErrorCode:         errorCode,
					ErrorSummary:      errorSummary,
					EvidenceQuality:   quality,
					ObservedAt:        finishedAt,
					StartedAt:         startedAt,
					FinishedAt:        finishedAt,
					EnvironmentID:     coord.Environment.Hash(),
					Stdout:            stdout,
					Stderr:            stderr,
					Count:             1,
					IsHighInformation: true,
				})
			}
		}
	}

	return recordErr
}

func sanitizedStreamEvidence(raw string, truncated bool) domain.CLIStreamEvidence {
	if strings.TrimSpace(raw) == "" {
		return domain.CLIStreamEvidence{Truncated: truncated}
	}
	san := sanitizer.Sanitize(raw, domain.StageProjectProcess, nil)
	excerpt, excerptTruncated := sanitizer.CLIOutputExcerpt(san.Template)
	return domain.CLIStreamEvidence{
		Fingerprint: san.Fingerprint,
		Excerpt:     excerpt,
		Truncated:   truncated || excerptTruncated,
	}
}

// cliEnvironment is the fingerprint a CLI observation is filed under.
//
// A CLI tool is routinely run where no project answers for it: outside any
// repository, before `npm install`, in a directory whose ecosystem no
// adapter detects. No scan at all leaves every axis empty; a scan that
// detected no adapter still leaves the ecosystem empty, because only an
// adapter names one. Either way the server refuses the whole batch
// ("environment requires ecosystem, os and arch") and the observation is
// lost — and that is exactly the population worth measuring.
//
// What the scan did establish is authoritative and is never overwritten;
// only the empty axes are answered, from the host that actually ran the
// command. An ecosystem the scan could not name is "generic" — the same
// namespace the CLI coordinate itself lives in, and an honest statement
// that no registry ecosystem was in play rather than a guess at one.
func cliEnvironment(res *scanner.ScanResult) domain.EnvironmentFingerprint {
	var env domain.EnvironmentFingerprint
	if res != nil {
		env = res.Env
	}
	if env.Ecosystem == "" {
		env.Ecosystem = domain.EcosystemGeneric
	}
	if env.OS != "" && env.Arch != "" {
		return env
	}
	host := csxenv.Host()
	if env.OS == "" {
		// The bucket describes the OS, so it travels with it and never
		// gets attached to an OS this host did not report.
		env.OS, env.OSVersionBucket = host.OS, host.OSVersionBucket
	}
	if env.Arch == "" {
		env.Arch = host.Arch
	}
	return env
}

func cliEvidenceQuality(coord domain.CLIExperienceCoordinate, startedAt, finishedAt string,
	term domain.FailureTermination, exitCode int) domain.EvidenceQuality {
	if term.Kind == "" && exitCode == 0 {
		code := 0
		term = domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}
	}
	if coord.ToolVersion != "" && coord.Shell != "" && coord.Environment.OS != "" &&
		coord.Environment.Arch != "" && startedAt != "" && finishedAt != "" && term.Structured() {
		return domain.EvidenceComplete
	}
	return domain.EvidencePartial
}

func (r *Recorder) recordRun(ctx context.Context, dir string, res *scanner.ScanResult,
	profile scanner.CommandProfile, failed bool, term domain.FailureTermination, failureTail string, classified []classifiedFailure) error {
	if res == nil {
		return nil
	}
	now := time.Now().UTC()
	epoch := now.Format("2006-01-02")
	epochMonth := now.Format("2006-01")

	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}
	bucket := r.Ident.ProjectBucket(absDir, epochMonth)

	if err := r.DB.SaveEnvironment(ctx, res.Env); err != nil {
		return err
	}
	envHash := res.Env.Hash()

	// Upsert the full inventory locally; collect the PUBLIC subset.
	public := map[string]domain.PURL{}
	direct := map[string]bool{}
	var publicNames []string
	for _, p := range res.Packages {
		if err := r.DB.UpsertPackage(ctx, p.PURL, p.Publicness); err != nil {
			return err
		}
		if p.Publicness == scanner.PublicnessPublic {
			key := p.PURL.String()
			// An excluded package leaves nothing: no observation, no symbol
			// row, nothing to batch. The setting was consulted by nothing
			// at all before this, so a user who asked for a package to stay
			// private still uploaded it.
			if r.Cfg.IsExcluded(key, p.PURL.Ecosystem, p.PURL.Name) {
				continue
			}
			if _, dup := public[key]; !dup {
				public[key] = p.PURL
				publicNames = append(publicNames, p.PURL.Name)
			}
			// Direct wins over transitive: the same package can appear both
			// ways in one resolution, and having been chosen is the fact
			// worth keeping.
			if p.Direct {
				direct[key] = true
			}
		}
	}
	// The other versions of each library present in THIS resolution. Computed
	// from the whole scan because that is the only place the whole lockfile
	// exists: the server receives one package per record.
	coresident := coresidentVersions(publicPURLs(public))
	// Who pulled what, when this ecosystem's lockfile says. Both ends public:
	// the rule is applied here, where the edges are chosen.
	edges := publicEdges(res.Edges, public)

	publicKeys := make([]string, 0, len(public))
	for k := range public {
		publicKeys = append(publicKeys, k)
	}
	sort.Strings(publicKeys)

	// Package-level project-bucket sighting (symbol ""), public only.
	// The batcher reads these to fill ObservationBatch.ProjectBucket.
	for _, key := range publicKeys {
		if err := r.DB.RecordSymbolUsage(ctx, public[key], "", domain.SymbolUnknown, bucket); err != nil {
			return err
		}
	}

	if !profile.Known && len(classified) == 0 {
		// Unclassified command: usage evidence only (C14).
		//
		// Nothing was built, but a lockfile WAS resolved, and USED is exactly
		// "this resolution contained X" — so what the resolution said belongs
		// here at least as much as on a build. It used to record the package
		// and drop the rest, and in production every observation arriving was
		// USED, so the edges and the direct flag were computed on every scan
		// and thrown away.
		for _, key := range publicKeys {
			err := r.DB.RecordObservation(ctx, usageObsKey(usageFacts{
				Epoch:      epoch,
				PURL:       key,
				EnvHash:    envHash,
				Direct:     direct[key],
				Coresident: coresident[key],
				DependsOn:  edges[key],
			}), 1)
			if err != nil {
				return err
			}
		}
		return nil
	}

	type outcome struct {
		stage   domain.Stage
		result  domain.Result
		failure domain.FailureEvidence
	}
	outcomes := []outcome{{stage: profile.Stage, result: domain.ResultPass}}
	if failed {
		outcomes = outcomes[:0]
		if len(classified) > 0 {
			for _, event := range classified {
				outcomes = append(outcomes, outcome{stage: event.stage, result: domain.ResultFail, failure: event.evidence})
			}
		} else {
			outcomes = append(outcomes, outcome{stage: profile.Stage, result: domain.ResultFail,
				failure: sanitizer.SanitizeFailure(failureTail, profile.Stage, term, publicNames)})
		}
	}

	for _, key := range publicKeys {
		for _, observed := range outcomes {
			err := r.DB.RecordObservation(ctx, localdb.ObsKey{
				Epoch:              epoch,
				PURL:               key,
				EnvHash:            envHash,
				Stage:              observed.stage,
				Result:             observed.result,
				ErrorFP:            observed.failure.Fingerprint,
				ErrorCode:          observed.failure.ErrorCode,
				TerminationKind:    observed.failure.TerminationKind,
				ExitCode:           observed.failure.ExitCode,
				Signal:             observed.failure.Signal,
				TimeoutMillis:      observed.failure.TimeoutMillis,
				ErrorSummary:       observed.failure.ErrorSummary,
				EvidenceQuality:    observed.failure.EvidenceQuality,
				OuterCommand:       observed.failure.OuterCommand,
				OuterStage:         observed.failure.OuterStage,
				ActualToolchain:    observed.failure.ActualToolchain,
				StageEvidence:      observed.failure.StageEvidence,
				FailureEvidenceGap: observed.failure.EvidenceGap,
				Direct:             direct[key],
				Coresident:         coresident[key],
				DependsOn:          edges[key],
			}, 1)
			if err != nil {
				return err
			}
		}
	}

	for _, u := range res.Symbols {
		key := u.Package.String()
		if _, isPublic := public[key]; !isPublic || u.Family == "" {
			continue // PRIVATE/UNKNOWN symbols never enter any evidence table
		}
		if err := r.DB.RecordSymbolUsage(ctx, u.Package, u.Family, u.Confidence, bucket); err != nil {
			return err
		}
		for _, observed := range outcomes {
			err := r.DB.RecordObservation(ctx, localdb.ObsKey{
				Epoch:              epoch,
				PURL:               key,
				Symbol:             u.Family,
				SymbolConfidence:   u.Confidence,
				EnvHash:            envHash,
				Stage:              observed.stage,
				Result:             observed.result,
				ErrorFP:            observed.failure.Fingerprint,
				ErrorCode:          observed.failure.ErrorCode,
				TerminationKind:    observed.failure.TerminationKind,
				ExitCode:           observed.failure.ExitCode,
				Signal:             observed.failure.Signal,
				TimeoutMillis:      observed.failure.TimeoutMillis,
				ErrorSummary:       observed.failure.ErrorSummary,
				EvidenceQuality:    observed.failure.EvidenceQuality,
				OuterCommand:       observed.failure.OuterCommand,
				OuterStage:         observed.failure.OuterStage,
				ActualToolchain:    observed.failure.ActualToolchain,
				StageEvidence:      observed.failure.StageEvidence,
				FailureEvidenceGap: observed.failure.EvidenceGap,
				Direct:             direct[key],
			}, 1)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
