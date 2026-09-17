package domain

import (
	"errors"
	"regexp"
	"strings"
)

// Execution footprints (#318).
//
// A caller that reached the network over plain HTTPS -- no csx binary, no
// MCP host, no local offer token -- may still have run the sample it was
// handed. What it can honestly tell us is small: which sample, whether it
// ran, and roughly where. Everything here is a closed vocabulary or a
// short lowercase token so the endpoint can never become a place to put a
// path, a log or a project name.

// FootprintOutcome is what the caller says happened. The set is closed:
// there is no free-text outcome and no "partial".
type FootprintOutcome string

const (
	FootprintPass        FootprintOutcome = "pass"
	FootprintFail        FootprintOutcome = "fail"
	FootprintCouldNotRun FootprintOutcome = "could_not_run"
)

// FootprintOutcomes lists the accepted outcomes in documentation order.
func FootprintOutcomes() []string {
	return []string{string(FootprintPass), string(FootprintFail), string(FootprintCouldNotRun)}
}

// FootprintStage is the coarse stage the caller reached. These are the
// stages a person can name from the outside; the finer evidence stages
// (RESOLVE, SYMBOL, CONTRACT ...) belong to the sanitizer and the verifier,
// which know what actually ran.
type FootprintStage string

const (
	FootprintStageBuild     FootprintStage = "build"
	FootprintStageTypecheck FootprintStage = "typecheck"
	FootprintStageTest      FootprintStage = "test"
	FootprintStageRuntime   FootprintStage = "runtime"
)

// FootprintStages lists the accepted stages in documentation order.
func FootprintStages() []string {
	return []string{string(FootprintStageBuild), string(FootprintStageTypecheck), string(FootprintStageTest), string(FootprintStageRuntime)}
}

// FootprintEnvironment is the bounded projection of an environment a
// footprint may carry: the same four dimensions ObservedEnvironment shows
// on a miss, and nothing a fingerprint could be built from.
type FootprintEnvironment struct {
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
	Runtime        string `json:"runtime,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
}

// ExecutionFootprint is the request body of POST /v1/footprints/execution.
type ExecutionFootprint struct {
	SchemaVersion int                  `json:"schemaVersion"`
	SampleID      string               `json:"sampleId"`
	Outcome       FootprintOutcome     `json:"outcome"`
	Stage         FootprintStage       `json:"stage,omitempty"`
	Environment   FootprintEnvironment `json:"environment,omitempty"`
	// FailureFingerprint is the 64-hex normalized fingerprint from a
	// knownFailures entry the caller matched, or one it computed with the
	// same contract. It is a hash or it is refused: a sentence, a path or a
	// log line does not fit this field on purpose.
	FailureFingerprint string `json:"failureFingerprint,omitempty"`
}

// footprintToken is what an environment dimension may look like: a short
// lowercase identifier such as "linux", "arm64", "node", "22.11.0" or
// "python3.12". Spaces, slashes, colons and anything upper-case are out --
// a value that needs them is a description, not a coordinate.
var footprintToken = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,31}$`)

var footprintFingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Errors a footprint can be refused with. They are sentences the caller can
// act on; none of them echoes the offending value back.
var (
	ErrFootprintSchema      = errors.New("execution footprint schemaVersion must be 1")
	ErrFootprintSampleID    = errors.New("execution footprint requires a sampleId of the form sha256:<64 hex>")
	ErrFootprintOutcome     = errors.New("execution footprint outcome must be one of: " + strings.Join(FootprintOutcomes(), " | "))
	ErrFootprintStage       = errors.New("execution footprint stage must be one of: " + strings.Join(FootprintStages(), " | "))
	ErrFootprintEnvironment = errors.New("execution footprint environment values must be short lowercase tokens (letters, digits, . _ + -), at most 32 characters")
	ErrFootprintFingerprint = errors.New("execution footprint failureFingerprint must be a 64-character hex fingerprint; never paste an error message")
)

// Normalize trims and lower-cases the token fields so that "Linux" and
// "linux " are the same coordinate. It does not validate.
func (f ExecutionFootprint) Normalize() ExecutionFootprint {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	f.SampleID = strings.TrimSpace(f.SampleID)
	f.Outcome = FootprintOutcome(norm(string(f.Outcome)))
	f.Stage = FootprintStage(norm(string(f.Stage)))
	f.Environment.OS = norm(f.Environment.OS)
	f.Environment.Arch = norm(f.Environment.Arch)
	f.Environment.Runtime = norm(f.Environment.Runtime)
	f.Environment.RuntimeVersion = norm(f.Environment.RuntimeVersion)
	f.FailureFingerprint = norm(f.FailureFingerprint)
	return f
}

// Validate refuses anything outside the closed vocabulary. It runs on the
// normalized form, so call Normalize first.
func (f ExecutionFootprint) Validate() error {
	if f.SchemaVersion != 1 {
		return ErrFootprintSchema
	}
	if !IsSampleID(f.SampleID) {
		return ErrFootprintSampleID
	}
	switch f.Outcome {
	case FootprintPass, FootprintFail, FootprintCouldNotRun:
	default:
		return ErrFootprintOutcome
	}
	switch f.Stage {
	case "", FootprintStageBuild, FootprintStageTypecheck, FootprintStageTest, FootprintStageRuntime:
	default:
		return ErrFootprintStage
	}
	for _, v := range []string{f.Environment.OS, f.Environment.Arch, f.Environment.Runtime, f.Environment.RuntimeVersion} {
		if v != "" && !footprintToken.MatchString(v) {
			return ErrFootprintEnvironment
		}
	}
	if f.FailureFingerprint != "" && !footprintFingerprint.MatchString(f.FailureFingerprint) {
		return ErrFootprintFingerprint
	}
	return nil
}

// IsSampleID reports whether s has the shape of a content-addressed sample
// id: "sha256:" followed by exactly 64 lowercase hex digits.
func IsSampleID(s string) bool {
	const prefix = "sha256:"
	if len(s) != len(prefix)+64 || !strings.HasPrefix(s, prefix) {
		return false
	}
	return footprintFingerprint.MatchString(s[len(prefix):])
}
