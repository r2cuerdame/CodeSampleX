package domain

import "testing"

func passFailReport() AnomalyReport {
	return AnomalyReport{
		SchemaVersion: AnomalyReportSchemaVersion,
		AnomalyType:   AnomalyCSXPassLocalFail,
		Package:       "pkg:npm/axios@1.12.0",
		Symbol:        "axios.post",
		Environment: EnvironmentFingerprint{
			Ecosystem: "npm", OS: "linux", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22.18.0", Libc: "musl",
		},
		SampleID:      "sha256:" + hex64('a'),
		CSXObserved:   AnomalyObservation{Result: "PASS", Stage: "contract"},
		LocalObserved: AnomalyObservation{Result: "FAIL", Stage: "test"},
		Reproducible:  "yes",
		Confidence:    "high",
		ErrorCode:     "ERR_MODULE_NOT_FOUND",
	}
}

func hex64(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

func TestAConcreteMismatchIsAccepted(t *testing.T) {
	r := passFailReport().Normalize()
	if err := r.Validate(); err != nil {
		t.Fatalf("a CSX PASS against a local FAIL at the same coordinate must be accepted, got %v", err)
	}
}

// The single most important refusal: an LLM can produce suspicion for free,
// and a report with nothing measured behind it is exactly that.
func TestAPureHypothesisIsRejected(t *testing.T) {
	r := passFailReport()
	r.LocalObserved = AnomalyObservation{Detail: "I think this is wrong"}
	r.LLMHypothesis = "the version resolution is probably broken"
	if err := r.Normalize().Validate(); err == nil {
		t.Fatal("a report with no local PASS/FAIL must be rejected")
	} else if err != ErrAnomalyNoLocalObs {
		t.Fatalf("want ErrAnomalyNoLocalObs, got %v", err)
	}
}

func TestAgreementIsNotAnAnomaly(t *testing.T) {
	r := passFailReport()
	r.CSXObserved.Result = "FAIL" // both sides say FAIL: nothing disagrees
	if err := r.Normalize().Validate(); err != ErrAnomalyNoMismatch {
		t.Fatalf("want ErrAnomalyNoMismatch, got %v", err)
	}
}

func TestARepeatedMissAloneIsNotAnAnomaly(t *testing.T) {
	r := passFailReport()
	r.AnomalyType = AnomalyRepeatedNoSafeMatch
	r.CSXObserved = AnomalyObservation{Result: "UNKNOWN"}
	r.LocalObserved = AnomalyObservation{Result: "FAIL", Stage: "test"}
	r.ErrorFingerprint = ""
	if err := r.Normalize().Validate(); err != ErrAnomalyNoSafeMatchAlone {
		t.Fatalf("NO_SAFE_MATCH with no reproducible failure attached must be refused, got %v", err)
	}

	r.ErrorFingerprint = "sha256:" + hex64('b')
	r.Reproducible = "yes"
	if err := r.Normalize().Validate(); err != nil {
		t.Fatalf("NO_SAFE_MATCH WITH a reproducible public failure is admissible, got %v", err)
	}
}

func TestAReportWithNoPublicCoordinateIsRejected(t *testing.T) {
	r := passFailReport()
	r.Package = "internal-billing-lib"
	if err := r.Normalize().Validate(); err != ErrAnomalyPackage {
		t.Fatalf("want ErrAnomalyPackage, got %v", err)
	}
}

func TestRelatedIDsCannotSmuggleAnUncheckedPackageCoordinate(t *testing.T) {
	r := passFailReport()
	r.RelatedIDs = []string{"pkg:npm/@private/acme-billing@1.0.0"}
	if err := r.Normalize().Validate(); err != ErrAnomalyIdentifier {
		t.Fatalf("a package PURL in relatedIds bypassed the checked package field: %v", err)
	}
}

// Dedupe is the whole defence against one bad answer queuing a thousand
// containers, so the key must survive cosmetic differences.
func TestTheFingerprintIgnoresHowTheModelExplainedIt(t *testing.T) {
	a := passFailReport()
	a.LLMHypothesis = "probably a peer dependency"
	a.Confidence = "high"

	b := passFailReport()
	b.LLMHypothesis = "almost certainly an ESM/CJS interop bug"
	b.Confidence = "low"
	b.AnomalyType = "CSX-Pass-Local-Fail" // same type, different spelling

	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("two reports of the same mismatch must share one fingerprint")
	}
}

func TestTheFingerprintSeparatesDifferentCoordinates(t *testing.T) {
	a := passFailReport()
	b := passFailReport()
	b.Package = "pkg:npm/axios@1.11.0"
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("a different exact version is a different anomaly")
	}
	c := passFailReport()
	c.Environment.Libc = "glibc"
	if a.Fingerprint() == c.Fingerprint() {
		t.Fatal("musl and glibc are different compatibility populations")
	}
	d := passFailReport()
	d.Environment.RuntimeVersion = "22.18.7" // same node 22 line
	if a.Fingerprint() != d.Fingerprint() {
		t.Fatal("a patch-level difference inside one runtime line is the same anomaly")
	}
}

func receiptWith(contract string, env EnvironmentFingerprint) VerificationReceipt {
	return VerificationReceipt{
		Stages:      map[string]string{"resolve": "PASS", "compile": "PASS", "contract": contract},
		Environment: env,
	}
}

func TestAReproductionThatAgreesWithTheReporterConfirms(t *testing.T) {
	r := passFailReport()
	verdict, decided := AnomalyVerdictFromReceipt(r, receiptWith("FAIL", r.Environment))
	if !decided || verdict != AnomalyVerdictCSXDefect {
		t.Fatalf("a clean re-run that fails where CSX served a pass is our defect; got %q decided=%v", verdict, decided)
	}
}

func TestAReproductionThatAgreesWithTheNetworkClosesTheReport(t *testing.T) {
	r := passFailReport()
	verdict, decided := AnomalyVerdictFromReceipt(r, receiptWith("PASS", r.Environment))
	if !decided || verdict != AnomalyVerdictNotReproducible {
		t.Fatalf("want not-reproducible in the same environment, got %q decided=%v", verdict, decided)
	}

	elsewhere := r.Environment
	elsewhere.Libc = "glibc"
	verdict, decided = AnomalyVerdictFromReceipt(r, receiptWith("PASS", elsewhere))
	if !decided || verdict != AnomalyVerdictEnvironmentDifference {
		t.Fatalf("a pass on a different libc is an environment difference, got %q decided=%v", verdict, decided)
	}
}

func TestTheOppositeDirectionIsNewEvidenceRatherThanADefect(t *testing.T) {
	r := passFailReport()
	r.AnomalyType = AnomalyCSXFailLocalPass
	r.CSXObserved.Result = "FAIL"
	r.LocalObserved.Result = "PASS"
	verdict, decided := AnomalyVerdictFromReceipt(r, receiptWith("PASS", r.Environment))
	if !decided || verdict != AnomalyVerdictNewEvidence {
		t.Fatalf("want confirmed-new-evidence, got %q decided=%v", verdict, decided)
	}
}

func TestDataAnomaliesNeedTheirOwnEvidenceRatherThanAnOrdinaryContractReceipt(t *testing.T) {
	tests := []struct {
		name          string
		anomalyType   string
		csxObserved   string
		localObserved string
	}{
		{"dependency graph", AnomalyDependencyGraphUnknown, "UNKNOWN", "PASS"},
		{"evidence conflict", AnomalyEvidenceConflict, "FAIL", "PASS"},
		{"broken internal reference", AnomalyBrokenInternalReference, "UNKNOWN", "PASS"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := passFailReport()
			r.AnomalyType = tc.anomalyType
			r.CSXObserved.Result = tc.csxObserved
			r.LocalObserved.Result = tc.localObserved
			if err := r.Normalize().Validate(); err != nil {
				t.Fatalf("fixture is not admissible: %v", err)
			}
			if verdict, decided := AnomalyVerdictFromReceipt(r, receiptWith(tc.localObserved, r.Environment)); decided {
				t.Fatalf("an ordinary contract receipt confirmed %s as %q without measuring it", tc.name, verdict)
			}
		})
	}
}

func TestEveryAnomalyTypeRequiresTheMismatchItNames(t *testing.T) {
	for _, anomalyType := range []string{AnomalyEvidenceConflict, AnomalySymbolSignatureMismatch} {
		t.Run(anomalyType+" agreement", func(t *testing.T) {
			r := passFailReport()
			r.AnomalyType = anomalyType
			r.CSXObserved.Result = "PASS"
			r.LocalObserved.Result = "PASS"
			if err := r.Normalize().Validate(); err != ErrAnomalyNoMismatch {
				t.Fatalf("PASS/PASS must be refused, got %v", err)
			}
		})
	}
	for _, anomalyType := range []string{AnomalyDependencyGraphUnknown, AnomalyBrokenInternalReference} {
		t.Run(anomalyType+" concrete result", func(t *testing.T) {
			r := passFailReport()
			r.AnomalyType = anomalyType
			if err := r.Normalize().Validate(); err != ErrAnomalyNoMismatch {
				t.Fatalf("an unknown-style anomaly with CSX PASS must be refused, got %v", err)
			}
			r.CSXObserved.Result = "UNKNOWN"
			if err := r.Normalize().Validate(); err != nil {
				t.Fatalf("CSX UNKNOWN plus a concrete local observation must be admissible, got %v", err)
			}
		})
	}
}

// A resolve failure measures the verifier, not the sample. Turning that into
// a verdict would be inventing one.
func TestAContractThatNeverRanDecidesNothing(t *testing.T) {
	r := passFailReport()
	receipt := VerificationReceipt{
		Stages:      map[string]string{"resolve": "FAIL", "contract": "SKIPPED"},
		Environment: r.Environment,
	}
	if verdict, decided := AnomalyVerdictFromReceipt(r, receipt); decided {
		t.Fatalf("a skipped contract must not produce a verdict, got %q", verdict)
	}
}

// The hypothesis is stored and shown; it must never move the verdict.
func TestAWrongHypothesisDoesNotChangeTheVerdict(t *testing.T) {
	r := passFailReport()
	base, _ := AnomalyVerdictFromReceipt(r, receiptWith("FAIL", r.Environment))
	r.LLMHypothesis = "this is definitely a CSX indexing bug and nothing to do with the package"
	r.Confidence = "low"
	r.Reproducible = "no"
	got, _ := AnomalyVerdictFromReceipt(r, receiptWith("FAIL", r.Environment))
	if got != base {
		t.Fatalf("the verdict moved with the reporter's prose: %q → %q", base, got)
	}
	if !AnomalyVerdictConfirmed(got) {
		t.Fatalf("the local facts must survive a wrong hypothesis; got %q", got)
	}
}

func TestOnlyConfirmedVerdictsMayPromote(t *testing.T) {
	for _, v := range []string{
		AnomalyVerdictEnvironmentDifference, AnomalyVerdictNotReproducible,
		AnomalyVerdictInsufficientEvidence, AnomalyVerdictDuplicate, "",
	} {
		if AnomalyVerdictConfirmed(v) {
			t.Fatalf("%q must not promote anything", v)
		}
	}
	for _, v := range []string{
		AnomalyVerdictCSXDefect, AnomalyVerdictCompatibilityBoundary, AnomalyVerdictNewEvidence,
	} {
		if !AnomalyVerdictConfirmed(v) {
			t.Fatalf("%q is a confirmation", v)
		}
	}
}
