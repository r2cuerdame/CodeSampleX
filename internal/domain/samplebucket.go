package domain

import "strings"

// SampleProjectBucket is a sample id in the shape the evidence store accepts
// as a project bucket.
//
// A bucket may be 64 bytes; "sha256:" plus 64 hex is 71. The digest alone is
// exactly 64 and identifies the sample just as well, so nothing about "many
// receipts for one sample are one project" changes. Carrying the prefix cost
// the first receipt backfill every one of its 9,883 observations: the store
// refused them all and the run reported it as a bare count.
//
// It lives in domain because two sides now derive it — the server turning a
// receipt into observations, and the verifier reporting the dependency tree it
// resolved. Those two must agree exactly: a verification whose edges landed in
// a different bucket from its own observations would be counted as two
// projects, and the count of projects is the whole meaning of the number.
func SampleProjectBucket(sampleID string) string {
	return strings.TrimPrefix(sampleID, "sha256:")
}
