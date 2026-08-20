package evidence

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// The USED path runs for an unclassified command: nothing was built, but a
// lockfile WAS resolved. It recorded the package and dropped everything the
// resolution said about it — whether the project chose it, what else was
// installed at another version, and what it pulled.
//
// That is the wrong way round. USED is precisely "this resolution contained
// X", so the facts about the resolution belong there at least as much as on
// a build. In production every observation arriving was USED, so the edges
// and the direct flag were being computed on every scan and thrown away.
func TestUsageOnlyObservationsCarryWhatTheResolutionSaid(t *testing.T) {
	key := usageObsKey(usageFacts{
		Epoch: "2026-08-20", PURL: "pkg:npm/a@1.2.0", EnvHash: "env",
		Direct: true, Coresident: []string{"1.0.0"}, DependsOn: []string{"pkg:npm/b@2.1.0"},
	})
	if key.Stage != domain.StageUsed || key.Result != domain.ResultPass {
		t.Fatalf("key = %+v, want a passing USED record", key)
	}
	if !key.Direct {
		t.Error("the project chose this package and the record does not say so")
	}
	if len(key.Coresident) != 1 || key.Coresident[0] != "1.0.0" {
		t.Errorf("coresident = %v, want the other version that was installed", key.Coresident)
	}
	if len(key.DependsOn) != 1 || key.DependsOn[0] != "pkg:npm/b@2.1.0" {
		t.Errorf("dependsOn = %v, want what it pulled", key.DependsOn)
	}
}

var _ = localdb.ObsKey{}
