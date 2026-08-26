package daemon

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/metricname"
)

// The local dashboard document (GET /local/v1/stats, and csx stats --json)
// is read by the user, by csx ui and by the get_local_stats MCP tool, which
// hands it to a model that will paraphrase it. A field named for people would
// come back out of that paraphrase as a claim about people.
//
// docs/activation-funnel.md §6 is the rule; internal/metricname is the rule
// as code.
func TestLocalStatsDocumentNamesNothingItCannotMeasure(t *testing.T) {
	for _, v := range metricname.Check(Stats{}) {
		t.Errorf("local stats field %q: %s — %s", v.Field, v.Rule, v.Why)
	}
}
