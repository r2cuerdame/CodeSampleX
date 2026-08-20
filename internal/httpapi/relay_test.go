package httpapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The relay must be structurally incapable of impersonating a verified
// answer. A field named like a grade is all it would take for a model to read
// anonymous counts as a proof, so the type carries none.
func TestObservedPayloadHasNoGradeShapedFields(t *testing.T) {
	forbidden := map[string]bool{
		"Grade": true, "Confidence": true, "Score": true, "Evidence": true,
		"Case": true, "SampleID": true, "SampleStatus": true, "Exact": true,
		"Different": true, "Adaptation": true, "VerifiedOffer": true,
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(domain.ObservedReports{}),
		reflect.TypeOf(domain.ObservedCell{}),
		reflect.TypeOf(domain.ObservedError{}),
		reflect.TypeOf(domain.ObservedEnvironment{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if forbidden[f.Name] {
				t.Errorf("%s.%s borrows verification vocabulary", typ.Name(), f.Name)
			}
			if f.Type == reflect.TypeOf(domain.MatchGrade("")) {
				t.Errorf("%s.%s is a MatchGrade", typ.Name(), f.Name)
			}
		}
	}
	// And it must not be reachable from a result, only from the response.
	res := reflect.TypeOf(domain.SearchResult{})
	for i := 0; i < res.NumField(); i++ {
		if strings.Contains(res.Field(i).Type.String(), "Observed") {
			t.Errorf("SearchResult.%s carries relay data", res.Field(i).Name)
		}
	}
}

// Relaying must not invent a grade. NO_SAFE_MATCH keeps its meaning —
// nothing here has been proven for your case — and the payload is the
// difference between saying that with an empty hand and saying it while
// handing over what is already known.
func TestNoNewMatchGradeWasIntroduced(t *testing.T) {
	if len(gradeRank) != 5 {
		t.Fatalf("gradeRank has %d entries, want the original five", len(gradeRank))
	}
	for _, g := range []domain.MatchGrade{
		domain.GradeExact, domain.GradeCompatible, domain.GradeAdaptationRequired,
		domain.GradeReferenceOnly, domain.GradeNoSafeMatch,
	} {
		if _, ok := gradeRank[g]; !ok {
			t.Errorf("grade %q left the ladder", g)
		}
	}
}

// The environment projection is closed. The full fingerprint carries distro,
// libc version, container runtime, virtualization, frameworks and CI; those
// against a stable anonymous id are a fingerprint, not a measurement.
func TestObservedEnvironmentCarriesOnlyThePublishedDimensions(t *testing.T) {
	want := map[string]bool{
		"OS": true, "Arch": true, "Runtime": true, "RuntimeVersion": true,
		"PackageManager": true, "Libc": true, "Context": true,
	}
	typ := reflect.TypeOf(domain.ObservedEnvironment{})
	if typ.NumField() != len(want) {
		t.Fatalf("projection has %d fields, want exactly %d", typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		if !want[typ.Field(i).Name] {
			t.Errorf("unpublished dimension %q travels", typ.Field(i).Name)
		}
	}
}
