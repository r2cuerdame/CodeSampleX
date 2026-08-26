package compatibility

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/metricname"
)

// GET /v1/stats is the public rollup, and its field names are quoted far away
// from the caveats printed beside them. So the names themselves have to be
// unable to claim a head count: this document counts observation records,
// samples and rotating anonymous buckets, and the README's promise that
// unique/active users and successful installs are NOT measured stays true
// only while no field here is named as though they were.
//
// docs/activation-funnel.md §6 is the rule; internal/metricname is the rule
// as code.
func TestPublicStatsDocumentNamesNothingItCannotMeasure(t *testing.T) {
	for _, v := range metricname.Check(StatsDoc{}) {
		t.Errorf("/v1/stats field %q: %s — %s", v.Field, v.Rule, v.Why)
	}
}

// peers and projectsMonth are the two fields most likely to be read as people.
// They are allowed because they are declared with their unit; this pins the
// declaration so removing it is a test failure rather than a silent loss of
// the only place that says what one unit is.
func TestTheBucketNounsOnThePublicDocumentStayDeclared(t *testing.T) {
	for _, name := range []string{"peers", "projectsMonth"} {
		if metricname.BucketNouns[name] == "" {
			t.Errorf("bucket noun %q lost its declared unit", name)
		}
	}
}
