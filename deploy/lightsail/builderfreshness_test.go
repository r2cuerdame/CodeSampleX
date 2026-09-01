package lightsail

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The builder-freshness gate must allow the time a full pass actually takes.
//
// The gate is right to exist: a server that cannot complete an aggregation
// pass is broken in a way no smoke test catches. But its budget was a fixed
// 900 polls x 2s = 30 minutes, and on 2026-09-01 that stopped being enough.
//
// Measured on production the same day. The server restarted at 19:39:12Z and
// the builder wrote its generatedAt at 20:28:42Z -- 49 minutes -- with
// CSX_SNAPSHOT_INTERVAL at 5m and RunOnce called immediately at startup, so
// that is the pass, not schedule latency. The deploy waited 30 minutes,
// threw, and rolled a perfectly healthy v0.1.97 server back to v0.1.96.
//
// What made the pass slow is capacity, not code: evidence_agg went from
// roughly 72k rows to 216k that day, on a host whose server container sits at
// 97% of its 768MiB limit with a load average of 3.69 on 2 vCPU.
//
// So the budget follows the measurement. The gate still demands a COMPLETE
// pass -- nothing is weakened -- it simply stops calling a slow host a broken
// one. If a pass ever exceeds this too, the number is wrong again and the
// answer is the same: measure, then set it.
func TestTheBuilderFreshnessBudgetCoversAMeasuredPass(t *testing.T) {
	raw, err := os.ReadFile("deploy.ps1")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	attempts := intAssignment(t, body, "builderFreshPollAttempts")
	seconds := intAssignment(t, body, "builderFreshPollSeconds")
	budget := attempts * seconds

	// The longest pass measured was 49 minutes. A budget at or under that
	// cannot pass on the corpus that produced it.
	const measuredPassSeconds = 49 * 60
	if budget <= measuredPassSeconds {
		t.Errorf("builder freshness budget is %ds (%d x %ds); the longest measured pass was %ds, "+
			"so every deploy fails and rolls back a healthy server",
			budget, attempts, seconds, measuredPassSeconds)
	}
	// And not unbounded: a deploy that can hang for hours is its own outage.
	if budget > 2*60*60 {
		t.Errorf("builder freshness budget is %ds; a deploy that waits over two hours is an outage of its own", budget)
	}
}

// When it does give up, it has to say what it waited for. The failure that
// rolled back v0.1.97 said only "the new server did not complete a fresh full
// builder pass" -- no duration, no builder timestamp -- so from the output
// alone a slow host and a broken builder look identical.
func TestTheBuilderFreshnessFailureSaysWhatItMeasured(t *testing.T) {
	raw, err := os.ReadFile("deploy.ps1")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	at := strings.Index(body, "did not complete a fresh full builder pass")
	if at < 0 {
		t.Fatal("the builder freshness gate is gone")
	}
	line := body[max(0, at-400):at]
	if !strings.Contains(body[at:min(len(body), at+300)], "waited") &&
		!strings.Contains(line, "waited") {
		t.Error("the refusal does not say how long it waited")
	}
}

func intAssignment(t *testing.T, body, name string) int {
	t.Helper()
	re := regexp.MustCompile(`\$` + name + `\s*=\s*(\d+)`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s is not assigned in deploy.ps1", name)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}
