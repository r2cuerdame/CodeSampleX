package serverstore

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A fixed public target may be observed. An arbitrary generic purl may not.
//
// ValidateBatch runs before the publicness gate and refused every generic
// ecosystem outright, so an Unreal project's observation was rejected with
// "ecosystem \"generic\" not in the public allowlist" and the publicness
// shortcut added for it never got a chance to run. Measured against
// production 2026-09-01: after the client and the ingest gate were both
// fixed, csx sync still landed nothing and the coordinate never appeared.
//
// The allowlist cannot simply gain "generic". That is the ecosystem an
// arbitrary name lives in, and letting it through wholesale would put
// unchecked coordinates on public pages -- the exact thing the allowlist is
// for. domain.IsWantedTarget is the boundary: a closed vocabulary of engines
// and SDKs, each requiring a concrete version.
func TestABatchMayNameAFixedPublicTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		purl   string
		accept bool
	}{
		{"the unreal engine", "pkg:generic/engine/unreal@5.5", true},
		{"unity", "pkg:generic/engine/unity@6000.0", true},
		{"an arbitrary generic name", "pkg:generic/something/made-up@1.0.0", false},
		{"a generic target with no version", "pkg:generic/engine/unreal", false},
		{"an npm package, unchanged", "pkg:npm/axios@1.12.0", true},
		{"an ecosystem nobody allows", "pkg:nuget/Newtonsoft.Json@13.0.3", false},
	} {
		err := ValidateBatch(batchNaming(tc.purl))
		if tc.accept && err != nil {
			t.Errorf("%s (%s): rejected: %v", tc.name, tc.purl, err)
		}
		if !tc.accept && err == nil {
			t.Errorf("%s (%s): accepted, want a refusal", tc.name, tc.purl)
		}
		if !tc.accept && err != nil && strings.Contains(tc.purl, "generic") &&
			!strings.Contains(err.Error(), "generic") {
			t.Errorf("%s: refusal does not name the ecosystem: %v", tc.name, err)
		}
	}
}

func batchNaming(purl string) domain.ObservationBatch {
	return domain.ObservationBatch{
		SchemaVersion: 1,
		Epoch:         "2026-09-01",
		AnonID:        strings.Repeat("a", 64),
		ProjectBucket: strings.Repeat("b", 64),
		Package:       purl,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "generic", OS: "windows", Arch: "x64",
		},
		Stage:            domain.StageUsed,
		Result:           domain.ResultPass,
		ObservationCount: 1,
	}
}
