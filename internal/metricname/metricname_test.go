package metricname

import (
	"strings"
	"testing"
)

// The rule exists because a count of records read as a count of people is the
// specific way these documents mislead. docs/activation-funnel.md §6 states
// it; this is the same rule as code.

type forbiddenDoc struct {
	Evidence    int64 `json:"evidence"`
	ActiveUsers int64 `json:"activeUsers"`
}

func TestAnActorNamedFieldIsRefused(t *testing.T) {
	v := Check(forbiddenDoc{})
	if len(v) != 1 {
		t.Fatalf("violations = %d, want 1: %+v", len(v), v)
	}
	if v[0].Field != "activeUsers" || v[0].Rule != RuleForbiddenActor {
		t.Fatalf("violation = %+v, want activeUsers/%s", v[0], RuleForbiddenActor)
	}
	if !strings.Contains(v[0].Why, "users") {
		t.Errorf("Why = %q, want it to name the offending token", v[0].Why)
	}
}

type actorSpellings struct {
	A int64 `json:"uniqueUsers"`
	B int64 `json:"dau"`
	C int64 `json:"successfulInstalls"`
	D int64 `json:"liveMcpSessions"`
	E int64 `json:"machines"`
}

// Every field the README currently promises is NOT measured has to be
// refused by name, not only the one spelling somebody thought of.
func TestEveryUnmeasuredClaimIsRefusedByName(t *testing.T) {
	v := Check(actorSpellings{})
	if len(v) != 5 {
		t.Fatalf("violations = %d, want 5: %+v", len(v), v)
	}
	for _, got := range v {
		if got.Rule != RuleForbiddenActor {
			t.Errorf("%s: rule = %s, want %s", got.Field, got.Rule, RuleForbiddenActor)
		}
	}
}

type bucketDoc struct {
	Peers         int64 `json:"peers"`
	ProjectsMonth int64 `json:"projectsMonth"`
}

// peers and projectsMonth are terms of art: rotating anonymous buckets. They
// are allowed because they are declared, and the declaration carries the unit.
func TestDeclaredBucketNounsPass(t *testing.T) {
	if v := Check(bucketDoc{}); len(v) != 0 {
		t.Fatalf("violations = %+v, want none", v)
	}
	for name := range BucketNouns {
		if strings.TrimSpace(BucketNouns[name]) == "" {
			t.Errorf("bucket noun %q has no unit note", name)
		}
	}
}

type undeclaredBucketDoc struct {
	PeerCount int64 `json:"peerCount"`
}

// A new spelling of a bucket noun must be declared deliberately, because the
// declaration is where the "not a head count" note lives.
func TestAnUndeclaredBucketNounIsRefused(t *testing.T) {
	v := Check(undeclaredBucketDoc{})
	if len(v) != 1 || v[0].Rule != RuleUndeclaredBucket {
		t.Fatalf("violations = %+v, want one %s", v, RuleUndeclaredBucket)
	}
}

type unlabelledEstimate struct {
	EstimatedReasoningAvoided int64 `json:"estimatedReasoningAvoided"`
}

type siblingLabelledEstimate struct {
	EstimatedReasoningAvoided int64 `json:"estimatedReasoningAvoided"`
	Estimated                 bool  `json:"estimated"`
}

type carriedStat struct {
	Estimated bool   `json:"estimated"`
	Value     int64  `json:"value"`
	Formula   string `json:"formula"`
}

type typeLabelledEstimate struct {
	EstimatedReasoningAvoided carriedStat `json:"estimatedReasoningAvoided"`
}

// An estimate that no consumer can tell apart from a measurement is the other
// half of the same failure.
func TestAnEstimateMustCarryItsLabel(t *testing.T) {
	v := Check(unlabelledEstimate{})
	if len(v) != 1 || v[0].Rule != RuleUnlabelledEstimate {
		t.Fatalf("violations = %+v, want one %s", v, RuleUnlabelledEstimate)
	}
	if v := Check(siblingLabelledEstimate{}); len(v) != 0 {
		t.Errorf("sibling estimated flag rejected: %+v", v)
	}
	if v := Check(typeLabelledEstimate{}); len(v) != 0 {
		t.Errorf("carried estimated flag rejected: %+v", v)
	}
}

type nestedDoc struct {
	Inner struct {
		Users int64 `json:"users"`
	} `json:"inner"`
}

// A nested document is still the document. The homepage reads one level down.
func TestNestedFieldsAreChecked(t *testing.T) {
	v := Check(nestedDoc{})
	if len(v) != 1 || v[0].Rule != RuleForbiddenActor {
		t.Fatalf("violations = %+v, want one %s", v, RuleForbiddenActor)
	}
	if v[0].Field != "inner.users" {
		t.Errorf("Field = %q, want the nested path", v[0].Field)
	}
}

type hiddenDoc struct {
	Users  int64 `json:"-"`
	hidden int64
}

// Only what a consumer can read is a claim to a consumer. An unexported field
// and a json:"-" field never reach the wire.
func TestFieldsThatNeverReachAConsumerAreNotClaims(t *testing.T) {
	d := hiddenDoc{}
	_ = d.hidden
	if v := Check(d); len(v) != 0 {
		t.Fatalf("violations = %+v, want none", v)
	}
}
