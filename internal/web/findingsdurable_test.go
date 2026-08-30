package web

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// R2C-133's product rule: a durable finding must not vanish merely because the
// corpus grew. The store side of that was fixed — the scan is keyset-paged
// rather than a newest-2,000 read — but the page still asked for at most
// derivedCap findings and kept the prefix, so the rule was broken one layer
// up: past 2,000 findings the oldest stopped being reachable at all.
//
// Production sat at 545 of 2,000, so the cut was in the future rather than
// visible, which is exactly why it survived a "no change needed" review.
func TestADurableFindingIsStillThereAfterTheCorpusOutgrowsAnyWindow(t *testing.T) {
	const oldest = "the-oldest-durable-finding"
	f := newFakeStore()
	for i := 0; i < 2500; i++ {
		f.derived = append(f.derived, DerivedFinding{
			Ecosystem: "npm", Subject: fmt.Sprintf("pkg%d@1.0.0", i),
			Believed: fmt.Sprintf("belief %d", i), Measured: "measured",
			SampleID: fmt.Sprintf("sha256:%04d", i),
		})
	}
	// Last, because that is where the walk puts it: the belief scan pages
	// forward from the newest sample, so the oldest finding is the one a
	// prefix cut reaches last and drops first. Putting it first would have
	// made this test pass with the window still in place, which is the
	// mistake that let the window survive a review.
	f.derived = append(f.derived, DerivedFinding{
		Ecosystem: "npm", Subject: "axios@1.12.0", Believed: oldest,
		Measured: "it returns a promise", SampleID: "sha256:oldest",
	})

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	// The derived cache warms off the request path, so the first GET is cold
	// by design. Ask until it is warm rather than sleeping a guessed amount.
	query := "/findings?q=" + strings.ReplaceAll(oldest, "-", "+")
	var body string
	for i := 0; i < 100; i++ {
		body = get(t, mux, query).Body.String()
		if strings.Contains(body, oldest) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("a durable finding disappeared once %d newer ones arrived; "+
		"the corpus grew and the page stopped being able to reach it", len(f.derived)-1)
}
