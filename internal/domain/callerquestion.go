package domain

import "strings"

// A sanitized failure is two texts in one envelope, and only one of them is
// the caller's.
//
// What the toolchain printed is the failure. The lines beside it —
// "failureEvent: stage=PROJECT_TEST toolchain=go/test outer=go test
// evidence=test-runner-diagnostic gap=", "termination: exit:1",
// "evidenceQuality: complete" — are coordinates CodeSampleX wrote about its
// own run. They are identical on every Go test failure on earth, so nothing
// in them distinguishes one caller's case from another's.
//
// Retrieval may read the whole envelope: ranking is a heuristic and is
// allowed to reach. The promotion gate may not, because it asks a question of
// a different kind — did the CALLER name this subject — and answering it from
// our own header answers yes for reasons the caller never said.
//
// That is not hypothetical. A `go test` failure whose only words were about
// an integer promoted a gRPC interceptor sample to REUSE_VERIFIED, justified
// as "your question names its package or symbol": the sample declares
// google.golang.org/grpc/test/bufconn, and the "test" in that import path met
// the "test" in our own toolchain= field. The rest of the header reaches just
// as far. PROJECT_TEST and PROJECT_COMPILE have the exact shape of a
// structured error code, so the header can share a diagnostic with a sample;
// and "stage", "evidence", "gap" and "complete" are four topic words a goal
// sentence gets to overlap for free.
//
// These keys are written by the run_observed_command failure path. A key that
// is renamed there and not here stops being stripped, which is why the
// regression test for this drives the real producer rather than a fixture.
var sanitizedCoordinateKeys = []string{
	"failureEvent: ",
	"errorCode: ",
	"fingerprint: ",
	"termination: ",
	"evidenceQuality: ",
}

// CallerQuestion is the part of Query the caller actually said: the query
// with CodeSampleX's own sanitized-failure coordinate lines removed.
//
// A query that carries none of them — a typed question, a hook's raw stderr —
// is returned unchanged, so this narrows exactly one producer and nothing
// else. A query that is nothing BUT coordinates becomes empty, which is the
// honest reading: a failure that printed no words asked no question, and a
// candidate promoted by one has been promoted by our own telemetry.
func (r SearchRequest) CallerQuestion() string {
	if !strings.Contains(r.Query, "\n") {
		if isSanitizedCoordinateLine(r.Query) {
			return ""
		}
		return r.Query
	}
	lines := strings.Split(r.Query, "\n")
	kept := make([]string, 0, len(lines))
	dropped := false
	for _, line := range lines {
		if isSanitizedCoordinateLine(line) {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	if !dropped {
		return r.Query
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func isSanitizedCoordinateLine(line string) bool {
	line = strings.TrimSpace(line)
	for _, key := range sanitizedCoordinateKeys {
		if strings.HasPrefix(line, key) {
			return true
		}
	}
	return false
}
