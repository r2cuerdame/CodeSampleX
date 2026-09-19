package fixclaims

import (
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Verdict is what one execution of the reproducer at one version in one
// environment established. It is copied from the receipt's contract result,
// never from the worker's opinion.
type Verdict string

const (
	VerdictPass Verdict = "PASS"
	VerdictFail Verdict = "FAIL"
	// VerdictUnrunnable is a release the reproducer could not be executed
	// against at all -- it does not resolve, does not build, or the runtime
	// the claim needs does not exist here. It says nothing about the bug.
	VerdictUnrunnable Verdict = "UNRUNNABLE"
)

// ValidVerdict reports whether v is one of the three verdicts.
func ValidVerdict(v Verdict) bool {
	return v == VerdictPass || v == VerdictFail || v == VerdictUnrunnable
}

// Environment is the bounded coordinate a run is filed under. It is the
// same four dimensions a footprint carries; it is bucketed, never exact,
// and it is part of the run's identity: a PASS on linux says nothing about
// windows, which is what PARTIAL_FIX exists to say.
type Environment struct {
	OS             string `json:"os"`
	Arch           string `json:"arch,omitempty"`
	Runtime        string `json:"runtime,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
}

// Key is the environment's identity string.
func (e Environment) Key() string {
	return strings.Join([]string{e.OS, e.Arch, e.Runtime, e.RuntimeVersion}, "|")
}

// Run is one execution of the reproducer. A PASS or FAIL run is only
// admissible with the receipt that proves it; the ingest checks that the
// receipt exists, belongs to SampleID, and reached the same verdict.
type Run struct {
	Version     string      `json:"version"`
	Environment Environment `json:"environment"`
	Verdict     Verdict     `json:"verdict"`
	// FailureFingerprint is the normalized CSX failure fingerprint of a FAIL
	// run. It is what makes two failures "the same bug": the fixed side
	// failing with a different fingerprint is not the bug surviving, it is
	// something else, and the evaluator says AMBIGUOUS rather than guessing.
	FailureFingerprint string `json:"failureFingerprint,omitempty"`
	ReceiptID          string `json:"receiptId,omitempty"`
	SampleID           string `json:"sampleId,omitempty"`
	// FarmSeconds is the wall clock the run cost, for the cost metric.
	FarmSeconds int64     `json:"farmSeconds,omitempty"`
	ObservedAt  time.Time `json:"observedAt"`
}

// PairOutcome is what the minimum useful proof -- one bad release, one
// claimed-fixed release, same reproducer, same environment -- established.
type PairOutcome string

const (
	PairPending              PairOutcome = ""
	PairReproducedAndFixed   PairOutcome = "REPRODUCED_AND_FIXED"
	PairReproducedNotFixed   PairOutcome = "REPRODUCED_NOT_FIXED"
	PairNotReproduced        PairOutcome = "NOT_REPRODUCED"
	PairFixedUnrunnable      PairOutcome = "FIXED_VERSION_UNRUNNABLE"
	PairBadUnrunnable        PairOutcome = "BAD_VERSION_UNRUNNABLE"
	PairEnvironmentDependent PairOutcome = "ENVIRONMENT_DEPENDENT"
	PairAmbiguous            PairOutcome = "AMBIGUOUS"
)

// Status is the semantic state a claim record is in. Only VERIFIED_FIX and
// PARTIAL_FIX are evidence; the API marks every other state verified=false.
type Status string

const (
	// StatusClaimedFix: upstream says it is fixed; CSX has not established
	// anything. The state every candidate is born in and the only one a
	// producer can put it in.
	StatusClaimedFix Status = "CLAIMED_FIX"
	// StatusReproducedBug: the reproducer FAILED on the bad release. The
	// fixed release is either untested or failed the same way.
	StatusReproducedBug Status = "REPRODUCED_BUG"
	// StatusVerifiedFix: same reproducer, same environment, FAIL on the bad
	// release and PASS on the claimed-fixed one, both under receipts.
	StatusVerifiedFix Status = "VERIFIED_FIX"
	// StatusPartialFix: reproduced in more than one environment, fixed in
	// some of them.
	StatusPartialFix Status = "PARTIAL_FIX"
	// StatusClaimNotReproduced: the bounded test could not make the bad
	// release fail. Explicit, and never a PASS for the fix.
	StatusClaimNotReproduced Status = "CLAIM_NOT_REPRODUCED"
	// StatusRegressed: the bug's fingerprint appeared again on a release
	// above a verified-good boundary.
	StatusRegressed Status = "REGRESSED"
)

// Statuses lists every status in documentation order.
func Statuses() []Status {
	return []Status{StatusClaimedFix, StatusReproducedBug, StatusVerifiedFix, StatusPartialFix, StatusClaimNotReproduced, StatusRegressed}
}

// ValidStatus reports whether s is a status this package can produce.
func ValidStatus(s Status) bool {
	for _, x := range Statuses() {
		if x == s {
			return true
		}
	}
	return false
}

// Verified reports whether a status is one the API may present as CSX
// evidence.
func (s Status) Verified() bool {
	return s == StatusVerifiedFix || s == StatusPartialFix
}

// EnvironmentResult is the pair outcome in one environment with the runs it
// rests on.
type EnvironmentResult struct {
	Environment        Environment `json:"environment"`
	Outcome            PairOutcome `json:"outcome"`
	BugFingerprint     string      `json:"bugFingerprint,omitempty"`
	BadRun             *Run        `json:"badRun,omitempty"`
	FixedRun           *Run        `json:"fixedRun,omitempty"`
	FirstObservedBad   string      `json:"firstObservedBad,omitempty"`
	LastObservedBad    string      `json:"lastObservedBad,omitempty"`
	FirstObservedGood  string      `json:"firstObservedGood,omitempty"`
	RegressedAtVersion string      `json:"regressedAtVersion,omitempty"`
}

// Evaluation is the pure result of reading every run recorded for a
// candidate. Nothing else decides a status.
type Evaluation struct {
	Status       Status              `json:"status"`
	PairOutcome  PairOutcome         `json:"pairOutcome,omitempty"`
	BadVersion   string              `json:"badVersion,omitempty"`
	GoodVersion  string              `json:"goodVersion,omitempty"`
	Environments []EnvironmentResult `json:"environments,omitempty"`
	// Evidence lists every receipt the status rests on, in run order.
	Evidence   []string  `json:"evidence,omitempty"`
	SampleIDs  []string  `json:"sampleIds,omitempty"`
	RunCount   int       `json:"runCount"`
	VerifiedAt time.Time `json:"verifiedAt,omitempty"`
}

// Evaluate reads every run for a candidate and returns the state the
// evidence supports. It is deterministic in the runs and the candidate's
// two claimed versions; the order runs arrived in does not matter.
//
// Per environment, the pair rule is:
//
//	bad UNRUNNABLE                        -> BAD_VERSION_UNRUNNABLE
//	fixed UNRUNNABLE                      -> FIXED_VERSION_UNRUNNABLE
//	bad PASS                              -> NOT_REPRODUCED
//	bad FAIL, fixed missing               -> (pending; status REPRODUCED_BUG)
//	bad FAIL, fixed PASS                  -> REPRODUCED_AND_FIXED
//	bad FAIL, fixed FAIL same fingerprint -> REPRODUCED_NOT_FIXED
//	bad FAIL, fixed FAIL other fingerprint-> AMBIGUOUS
//
// Across environments, REPRODUCED_AND_FIXED beside REPRODUCED_NOT_FIXED is
// PARTIAL_FIX, and any two different decided outcomes make the pair
// ENVIRONMENT_DEPENDENT. A FAIL carrying the bug's fingerprint at a version
// above a REPRODUCED_AND_FIXED boundary turns the whole record REGRESSED.
func Evaluate(c Candidate, runs []Run) Evaluation {
	c = c.Normalized()
	ev := Evaluation{Status: StatusClaimedFix, RunCount: len(runs)}
	if len(runs) == 0 {
		return ev
	}
	sorted := append([]Run(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if c := domain.CompareVersions(sorted[i].Version, sorted[j].Version); c != 0 {
			return c < 0
		}
		return sorted[i].ObservedAt.Before(sorted[j].ObservedAt)
	})
	byEnv := map[string][]Run{}
	var envOrder []string
	for _, r := range sorted {
		k := r.Environment.Key()
		if _, ok := byEnv[k]; !ok {
			envOrder = append(envOrder, k)
		}
		byEnv[k] = append(byEnv[k], r)
	}
	sort.Strings(envOrder)

	var (
		fixedSomewhere, notFixedSomewhere, notReproducedSomewhere, reproducedSomewhere, regressed bool
		decided                                                                                   []PairOutcome
		badVersion, goodVersion                                                                   string
		latest                                                                                    time.Time
	)
	for _, k := range envOrder {
		res := evaluateEnvironment(c, byEnv[k])
		ev.Environments = append(ev.Environments, res)
		switch res.Outcome {
		case PairReproducedAndFixed:
			fixedSomewhere = true
			reproducedSomewhere = true
		case PairReproducedNotFixed:
			notFixedSomewhere = true
			reproducedSomewhere = true
		case PairNotReproduced:
			notReproducedSomewhere = true
		case PairPending, PairFixedUnrunnable, PairAmbiguous:
			if res.BadRun != nil && res.BadRun.Verdict == VerdictFail {
				reproducedSomewhere = true
			}
		}
		if res.Outcome != PairPending {
			decided = append(decided, res.Outcome)
		}
		if res.RegressedAtVersion != "" {
			regressed = true
		}
		if res.FirstObservedBad != "" && (badVersion == "" || domain.CompareVersions(res.FirstObservedBad, badVersion) < 0) {
			badVersion = res.FirstObservedBad
		}
		if res.FirstObservedGood != "" && (goodVersion == "" || domain.CompareVersions(res.FirstObservedGood, goodVersion) > 0) {
			goodVersion = res.FirstObservedGood
		}
	}
	for _, r := range sorted {
		if r.ReceiptID != "" {
			ev.Evidence = append(ev.Evidence, r.ReceiptID)
		}
		if r.SampleID != "" && !contains(ev.SampleIDs, r.SampleID) {
			ev.SampleIDs = append(ev.SampleIDs, r.SampleID)
		}
		if r.ObservedAt.After(latest) {
			latest = r.ObservedAt
		}
	}

	ev.PairOutcome = combinedOutcome(decided)
	switch {
	case fixedSomewhere && notFixedSomewhere:
		ev.Status = StatusPartialFix
	case fixedSomewhere:
		ev.Status = StatusVerifiedFix
	case notFixedSomewhere || reproducedSomewhere:
		ev.Status = StatusReproducedBug
	case notReproducedSomewhere && ev.PairOutcome == PairNotReproduced:
		ev.Status = StatusClaimNotReproduced
	default:
		// Only unrunnable or ambiguous runs: nothing was established.
		ev.Status = StatusClaimedFix
	}
	if regressed && (ev.Status == StatusVerifiedFix || ev.Status == StatusPartialFix) {
		ev.Status = StatusRegressed
	}
	if ev.Status.Verified() || ev.Status == StatusRegressed {
		ev.BadVersion, ev.GoodVersion = badVersion, goodVersion
		ev.VerifiedAt = latest
	} else if ev.Status == StatusReproducedBug {
		ev.BadVersion = badVersion
	}
	return ev
}

// combinedOutcome folds per-environment outcomes into one. One decided
// outcome is itself; any two different ones are ENVIRONMENT_DEPENDENT,
// because the reproducer told two environments two different things and
// the record must not pick one.
func combinedOutcome(decided []PairOutcome) PairOutcome {
	if len(decided) == 0 {
		return PairPending
	}
	first := decided[0]
	for _, d := range decided[1:] {
		if d != first {
			return PairEnvironmentDependent
		}
	}
	return first
}

func evaluateEnvironment(c Candidate, runs []Run) EnvironmentResult {
	res := EnvironmentResult{Environment: runs[0].Environment}
	// The latest run at each version stands; a re-run supersedes.
	atVersion := map[string]Run{}
	var versions []string
	for _, r := range runs {
		if _, ok := atVersion[r.Version]; !ok {
			versions = append(versions, r.Version)
		}
		atVersion[r.Version] = r
	}
	badVersion := c.ClaimedBadVersion
	if badVersion == "" {
		// The candidate named no bad release: the lowest runnable release
		// below the fixed one plays the part, so a collector-inferred pair
		// still evaluates.
		for _, v := range versions {
			r := atVersion[v]
			if domain.CompareVersions(v, c.ClaimedFixedVersion) < 0 && r.Verdict != VerdictUnrunnable {
				badVersion = v
				break
			}
		}
	}
	if bad, ok := atVersion[badVersion]; ok {
		b := bad
		res.BadRun = &b
	}
	if fixed, ok := atVersion[c.ClaimedFixedVersion]; ok {
		f := fixed
		res.FixedRun = &f
	}
	switch {
	case res.BadRun == nil:
		res.Outcome = PairPending
	case res.BadRun.Verdict == VerdictUnrunnable:
		res.Outcome = PairBadUnrunnable
	case res.BadRun.Verdict == VerdictPass:
		res.Outcome = PairNotReproduced
	case res.FixedRun == nil:
		res.Outcome = PairPending
		res.BugFingerprint = res.BadRun.FailureFingerprint
	case res.FixedRun.Verdict == VerdictUnrunnable:
		res.Outcome = PairFixedUnrunnable
		res.BugFingerprint = res.BadRun.FailureFingerprint
	case res.FixedRun.Verdict == VerdictPass:
		res.Outcome = PairReproducedAndFixed
		res.BugFingerprint = res.BadRun.FailureFingerprint
	case res.FixedRun.FailureFingerprint == res.BadRun.FailureFingerprint:
		res.Outcome = PairReproducedNotFixed
		res.BugFingerprint = res.BadRun.FailureFingerprint
	default:
		res.Outcome = PairAmbiguous
		res.BugFingerprint = res.BadRun.FailureFingerprint
	}
	if res.BugFingerprint == "" {
		return res
	}
	// The boundary, read from every version run in this environment with
	// the bug's fingerprint: first and last release seen failing this way,
	// the first release above the first bad one seen passing, and any
	// release above THAT seen failing this way again.
	for _, v := range versions {
		r := atVersion[v]
		isBug := r.Verdict == VerdictFail && r.FailureFingerprint == res.BugFingerprint
		if isBug && res.FirstObservedBad == "" {
			res.FirstObservedBad = v
		}
		if isBug && res.FirstObservedGood == "" {
			res.LastObservedBad = v
		}
		if r.Verdict == VerdictPass && res.FirstObservedBad != "" && res.FirstObservedGood == "" {
			res.FirstObservedGood = v
		}
		if isBug && res.FirstObservedGood != "" && res.RegressedAtVersion == "" {
			res.RegressedAtVersion = v
		}
	}
	return res
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
