package lightsail

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Realized server output. The server writes at most one of these per second
// per class, so a window that refused ten thousand requests can contain three
// lines -- which is why the totals are on the line at all.
const (
	pressureLineQuiet = "2026/09/09 04:10:01 csx-server: db pressure path=/records class=interactive " +
		"cause=admission_refused pool_busy=0 query_timeout=0 admission_refused=1 deferred_refused=0 waited=0s " +
		"pool_busy_total=0 query_timeout_total=0 admission_refused_total=41 deferred_refused_total=0"
	pressureLineBusy = "2026/09/09 04:10:02 csx-server: db pressure path=/wanted class=interactive " +
		"cause=pool_busy+query_timeout pool_busy=3 query_timeout=2 admission_refused=0 deferred_refused=0 waited=3s " +
		"pool_busy_total=57 query_timeout_total=18 admission_refused_total=96 deferred_refused_total=4"
	pressureLineDeferred = "2026/09/09 04:10:03 csx-server: db pressure path=/npm/zod class=interactive " +
		"cause=deferred_refused pool_busy=0 query_timeout=0 admission_refused=0 deferred_refused=1 waited=0s " +
		"pool_busy_total=57 query_timeout_total=18 admission_refused_total=120 deferred_refused_total=9"
)

func pressureFixture() string {
	return strings.Join([]string{pressureLineQuiet, pressureLineBusy, pressureLineDeferred}, "\n")
}

// The whole lane in one test: the collector must read the counts the server
// kept, not the lines the rate limiter let through. On this fixture a line
// count says "2 pool-busy lines"; the server counted 57 refusals.
func TestCollectorDerivesPressureEventCountsFromCumulativeTotals(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	program := shellFunction(t, collector, "summarize_pressure") + "\nsummarize_pressure\n"

	got := runPressureShell(t, program, pressureFixture())
	for field, want := range map[string]string{
		"pool_busy_event_total":         "57",
		"query_timeout_event_total":     "18",
		"admission_refused_event_total": "120",
		"deferred_refused_event_total":  "9",
	} {
		if got[field] != want {
			t.Errorf("collector derived %s=%q, want %q (from %v)", field, got[field], want, got)
		}
	}
}

// A window with no pressure at all must produce zeros, not empty strings: the
// observer rejects the whole sample as malformed if any counter is not an
// integer, which would turn a quiet deploy into a failed observation.
func TestCollectorReportsZeroPressureEventsForAQuietWindow(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	program := shellFunction(t, collector, "summarize_pressure") + "\nsummarize_pressure\n"

	got := runPressureShell(t, program, "")
	for _, field := range []string{
		"pool_busy_event_total", "query_timeout_event_total",
		"admission_refused_event_total", "deferred_refused_event_total",
	} {
		if got[field] != "0" {
			t.Errorf("a quiet window produced %s=%q, want 0", field, got[field])
		}
	}
}

// The legacy fields are what every earlier observation on the tracking issue
// reported, so they must keep meaning exactly what they meant. The risk is
// specific: `pool_busy_total=57` contains the substring the legacy counter
// greps for, so a badly named total would silently inflate the old number.
func TestLegacyPressureLineCountsAreUnchangedByTheNewTotals(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	var program strings.Builder
	program.WriteString("pressure_log=$(cat)\n")
	for _, name := range []string{"pressure_lines", "pool_busy_events", "query_timeout_events"} {
		program.WriteString(collectorAssignment(t, collector, name) + "\n")
		program.WriteString("printf '" + name + "=%s\\n' \"$" + name + "\"\n")
	}

	got := runPressureShell(t, program.String(), pressureFixture())
	// Three lines; exactly one of them charged a real pool refusal, and one a
	// real statement timeout -- the same line.
	for field, want := range map[string]string{
		"pressure_lines":       "3",
		"pool_busy_events":     "1",
		"query_timeout_events": "1",
	} {
		if got[field] != want {
			t.Errorf("legacy %s changed to %q, want %q (from %v)", field, got[field], want, got)
		}
	}
}

// Two files, one contract. The server names the cumulative fields and the
// collector reads them; nothing else connects them, so a rename in either
// file must fail here rather than on production.
func TestCollectorReadsTheCumulativeFieldsTheServerActuallyWrites(t *testing.T) {
	server := readDeployFixture(t, "../../cmd/csx-server/dbclass.go")
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	for _, field := range []string{
		"pool_busy_total", "query_timeout_total",
		"admission_refused_total", "deferred_refused_total",
	} {
		if !strings.Contains(server, field+"=%d") {
			t.Errorf("server pressure line no longer emits %s", field)
		}
		if !strings.Contains(collector, `wanted["`+field+`"]`) {
			t.Errorf("collector no longer reads %s", field)
		}
	}
	// The refusals that never reach the pool are the ones #174 could not see.
	for _, site := range []string{"noteAdmissionRefusal", "noteDeferredRefusal"} {
		if !strings.Contains(readDeployFixture(t, "../../cmd/csx-server/webstore.go"), site+"(ctx)") {
			t.Errorf("the web store no longer counts refusals through %s", site)
		}
	}
	// The observer rejects a sample that is missing any required key, so a
	// key it requires and the collector never prints fails every deployment
	// observation rather than one assertion.
	observer := readDeployFixture(t, "observe-production.ps1")
	for _, key := range []string{
		"pool_busy_event_total", "query_timeout_event_total",
		"admission_refused_event_total", "deferred_refused_event_total",
	} {
		if !strings.Contains(observer, "'"+key+"'") {
			t.Errorf("observer does not require %s", key)
		}
		if !strings.Contains(collector, `printf '`+key+`=%s\n'`) {
			t.Errorf("observer requires %s but the collector never prints it", key)
		}
	}
}

// The new counts must reach the tracking issue, and the thresholds that decide
// incident acceptance must not move. Historical counters remain published;
// only proven observation-window errors are attributed to this observation.
func TestObserverPublishesPressureEventCountsWithoutMovingThresholds(t *testing.T) {
	observer := readDeployFixture(t, "observe-production.ps1")
	for _, required := range []string{
		"'pool_busy_event_total'", "'query_timeout_event_total'",
		"'admission_refused_event_total'", "'deferred_refused_event_total'",
		"poolBusyEventCount", "queryTimeoutEventCount",
		"admissionRefusedEventCount", "deferredRefusedEventCount",
		"- Pool-busy events (server counter):",
		"- Query-timeout events (server counter):",
		"- Admission-refused events (server counter):",
		"- Deferred-lane refusal events (server counter):",
	} {
		if !strings.Contains(observer, required) {
			t.Errorf("post-deploy evidence does not publish the event counts: missing %q", required)
		}
	}
	for _, threshold := range []string{
		`if ($evidence.pressure.windowQueryTimeoutEvents -ne 0) {`,
		`if ($evidence.pressure.windowPoolBusyEvents -ne 0) {`,
		`if ($evidence.pressure.windowMaxWaitSeconds -gt $MaxPressureWaitSeconds) {`,
		`if (-not $evidence.pressure.windowMeasured) {`,
		`$MaxPressureWaitSeconds = 3.0`,
	} {
		if !strings.Contains(observer, threshold) {
			t.Errorf("this lane moved a classification threshold: %q is gone", threshold)
		}
	}
}

func runPressureShell(t *testing.T, program, stdin string) map[string]string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil && runtime.GOOS == "windows" {
		sh, err = exec.LookPath(`C:\Program Files\Git\bin\sh.exe`)
	}
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	if runtime.GOOS == "windows" {
		program = "PATH='/usr/bin':$PATH\n" + program
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "-c", program)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("collector fragment failed: %v: %s", err, out)
	}
	parsed := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			parsed[key] = value
		}
	}
	return parsed
}

// shellFunction lifts one whole function out of the collector so the test runs
// the code that ships rather than a copy of it.
func shellFunction(t *testing.T, script, name string) string {
	t.Helper()
	script = strings.ReplaceAll(script, "\r\n", "\n")
	open := "\n" + name + "() {\n"
	start := strings.Index(script, open)
	if start < 0 {
		t.Fatalf("collector does not define %s() at top level", name)
	}
	start++
	end := strings.Index(script[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("collector %s() has no closing brace at top level", name)
	}
	return script[start : start+end+len("\n}\n")]
}

// collectorAssignment lifts one `name=$(...)` command substitution, so the
// legacy counters are tested as written rather than as remembered.
func collectorAssignment(t *testing.T, script, name string) string {
	t.Helper()
	script = strings.ReplaceAll(script, "\r\n", "\n")
	pattern := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(name) + `=\$\(.*\)$`)
	matches := pattern.FindAllString(script, -1)
	if len(matches) != 1 {
		t.Fatalf("collector defines %s with %d single-line assignments, want 1", name, len(matches))
	}
	return strings.TrimSpace(matches[0])
}
