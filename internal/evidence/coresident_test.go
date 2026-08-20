package evidence

import (
	"reflect"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func purls(list ...string) []domain.PURL {
	out := make([]domain.PURL, 0, len(list))
	for _, s := range list {
		p, err := domain.ParsePURL(s)
		if err != nil {
			panic(err)
		}
		out = append(out, p)
	}
	return out
}

// A resolution that installed one library at two versions has already said
// where to look, and it is the commonest reason a build does not work. The
// SERVER cannot see it: an ObservationBatch carries a single package, so a
// lockfile arrives already shredded and the finest grouping left is a whole
// day. The scanner holds the lockfile at once, so here it is an observation
// rather than an inference.
func TestCoresidentVersionsAreFoundWithinOneResolution(t *testing.T) {
	got := coresidentVersions(purls(
		"pkg:npm/ws@8.19.0",
		"pkg:npm/ws@7.5.0",
		"pkg:npm/axios@1.12.0",
	))
	if want := []string{"7.5.0"}; !reflect.DeepEqual(got["pkg:npm/ws@8.19.0"], want) {
		t.Errorf("ws@8.19.0 sees %v, want %v", got["pkg:npm/ws@8.19.0"], want)
	}
	if want := []string{"8.19.0"}; !reflect.DeepEqual(got["pkg:npm/ws@7.5.0"], want) {
		t.Errorf("ws@7.5.0 sees %v, want %v", got["pkg:npm/ws@7.5.0"], want)
	}
	if _, ok := got["pkg:npm/axios@1.12.0"]; ok {
		t.Error("a package present at one version was given co-residents")
	}
}

// Ecosystems do not share a namespace: gem/rack and npm/rack are different
// libraries, and pairing their versions would invent a conflict.
func TestCoresidenceDoesNotCrossEcosystems(t *testing.T) {
	got := coresidentVersions(purls("pkg:npm/rack@1.0.0", "pkg:gem/rack@3.2.7"))
	if len(got) != 0 {
		t.Errorf("co-residents = %v, want none across ecosystems", got)
	}
}

// Three versions at once is three pairs from one resolution, and each version
// must see the other two.
func TestEveryCoresidentVersionSeesTheRest(t *testing.T) {
	got := coresidentVersions(purls(
		"pkg:npm/fs-extra@10.1.0",
		"pkg:npm/fs-extra@11.3.1",
		"pkg:npm/fs-extra@11.4.0",
	))
	if want := []string{"11.3.1", "11.4.0"}; !reflect.DeepEqual(got["pkg:npm/fs-extra@10.1.0"], want) {
		t.Errorf("10.1.0 sees %v, want %v", got["pkg:npm/fs-extra@10.1.0"], want)
	}
	if len(got) != 3 {
		t.Errorf("entries = %d, want one per version", len(got))
	}
}

// The same version listed twice is one installation seen twice, not a
// collision with itself.
func TestARepeatedVersionIsNotACoresident(t *testing.T) {
	got := coresidentVersions(purls("pkg:npm/ws@8.19.0", "pkg:npm/ws@8.19.0"))
	if len(got) != 0 {
		t.Errorf("co-residents = %v, want none", got)
	}
}
