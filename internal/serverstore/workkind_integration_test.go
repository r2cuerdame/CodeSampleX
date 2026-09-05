package serverstore

import (
	"context"
	"testing"
	"time"
)

// The kind vocabulary lives twice: in AuthoringWorkKinds, and in an inline
// CHECK on authoring_assignments that only a migration can widen.
//
// They have drifted before, and the drift is invisible where it is cheapest
// to notice. 0013 wrote three kinds; the dependency closure began producing a
// fourth; every DEPENDENCY claim then failed its INSERT in production while
// every unit test passed, because the Fake has no constraint to violate and
// the worker only ever saw "claiming authoring work failed". 0022 repaired
// that one. This is what stops the fifth kind repeating it: one claim of each
// kind, against the real schema.
func TestIntegrationEveryOfferedWorkKindCanBeClaimed(t *testing.T) {
	ctx := context.Background()
	pg := openTestPG(t)
	now := time.Now().UTC().Truncate(time.Second)

	for i, kind := range AuthoringWorkKinds {
		sessionID := "session-kind-" + kind
		if err := pg.IssueAuthoringSessions(ctx, []AuthoringSessionRow{{
			TokenHash: "hash-kind-" + kind, SessionID: sessionID, Label: "kind test",
			IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
		}}, now); err != nil {
			t.Fatal(err)
		}
		candidate := WantedRow{
			Ecosystem: "npm", Name: "kindpkg", Version: "1." + string(rune('0'+i)) + ".0",
			Kind: kind, Score: 1,
		}
		work, ok, err := pg.ClaimAuthoringWork(ctx, sessionID, []WantedRow{candidate},
			now, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("kind %s: the schema refused work the scheduler can offer: %v", kind, err)
		}
		if !ok {
			t.Fatalf("kind %s: nothing was claimed", kind)
		}
		if work.Kind != kind {
			t.Errorf("kind %s: claimed %q instead", kind, work.Kind)
		}
	}
}
