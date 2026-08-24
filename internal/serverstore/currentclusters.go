package serverstore

import "github.com/r2cuerdame/codesamplex/internal/domain"

// A failure cluster is derived data, and migration 0024 deliberately does not
// clear it: emptying a production table is a destructive operation that does
// not belong in an unattended additive migration. So the rows written before
// structured failure evidence existed are still there, each keyed by its own
// pre-contract fingerprint.
//
// The current builder never writes those keys again. A hash produced before
// exit code, signal, timeout and normalized error were recorded is provenance,
// not a failure identity, so missing and legacy evidence collapse into one
// explicit evidence-gap row with an empty fingerprint. The preserved rows are
// therefore historical material sitting beside the live ones — and counting
// them is what took production from 17,737 to 35,488 cluster observations on a
// deployment where the FAIL total never moved.
//
// They are hidden here rather than deleted. Removing them is a separately
// authorized cleanup with its own rollback and evidence.
//
// CurrentFailureClusterPredicateSQL and IsCurrentFailureCluster are two
// spellings of one rule. The deploy transaction computes the ledger in shell
// against the same predicate, so a change here is a change to what the gate
// measures.
const CurrentFailureClusterPredicateSQL = `(COALESCE(evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete') OR COALESCE(error_fp,'') = '')`

// IsCurrentFailureCluster reports whether a stored cluster row is one the
// current builder still writes. An empty EvidenceQuality is a row written
// before the column existed and reads back as the PostgreSQL default.
func IsCurrentFailureCluster(c ClusterRow) bool {
	quality := c.EvidenceQuality
	if quality == "" {
		quality = string(domain.EvidenceLegacyIncomplete)
	}
	switch domain.EvidenceQuality(quality) {
	case domain.EvidenceMissing, domain.EvidenceLegacyIncomplete:
		return c.ErrorFingerprint == ""
	default:
		return true
	}
}
