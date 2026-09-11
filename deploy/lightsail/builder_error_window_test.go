package lightsail

import (
	"strings"
	"testing"
)

func TestBuilderErrorWindowPreservesHistoricalErrorsAndExactBoundaries(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	const lifetime = "2026-09-12T13:00:00.123456789Z"
	const start = "2026-09-12T14:30:00.123456789Z"
	const end = "2026-09-12T14:31:00.000000000Z"
	const historical = "2026-09-12T14:28:00Z compatibility: builder run failed: pre-existing error"
	const boundary = "2026-09-12T14:30:00.123456789Z compatibility: builder run failed after 5 retries: error"
	const latest = "2026-09-12T14:30:01Z compatibility: builder run: legacy error"
	for _, tc := range []struct {
		name, start, lifetime, end, status, log, before, during, wantStatus string
	}{
		{"quiet", start, lifetime, end, "complete", "", "0", "0", "complete"},
		{"historical error retained", start, lifetime, end, "complete", historical, "1", "0", "complete"},
		{"exact boundary is new", start, lifetime, end, "complete", boundary, "0", "1", "complete"},
		{"all error formats split", start, lifetime, end, "complete", historical + "\n" + boundary + "\n" + latest, "1", "2", "complete"},
		{"one nanosecond before", start, lifetime, end, "complete", strings.Replace(boundary, "123456789Z", "123456788Z", 1), "1", "0", "complete"},
		{"short fractional timestamp", start, lifetime, end, "complete", strings.Replace(boundary, "123456789Z", "2Z", 1), "0", "1", "complete"},
		{"equivalent fractional boundary", "2026-09-12T14:30:00.1Z", lifetime, end, "complete", strings.Replace(boundary, "123456789Z", "100000000Z", 1), "0", "1", "complete"},
		{"missing boundary", "", lifetime, end, "complete", historical, "0", "1", "unavailable"},
		{"future boundary", "2026-09-12T15:00:00Z", lifetime, end, "complete", historical, "0", "1", "unavailable"},
		{"boundary before server", "2026-09-12T13:00:00Z", lifetime, end, "complete", historical, "0", "1", "unavailable"},
		{"invalid calendar boundary", "2026-02-30T14:30:00Z", "2026-01-01T00:00:00Z", end, "complete", historical, "0", "1", "unavailable"},
		{"invalid calendar error", start, lifetime, end, "complete", strings.Replace(historical, "09-12", "09-31", 1), "0", "1", "unavailable"},
		{"unparseable error timestamp", start, lifetime, end, "complete", "missing-timestamp compatibility: builder run failed: error", "0", "1", "unavailable"},
		{"error beyond captured end", start, lifetime, end, "complete", strings.Replace(latest, "14:30:01", "14:31:01", 1), "0", "1", "unavailable"},
		{"error before server horizon", start, lifetime, end, "complete", strings.Replace(historical, "14:28:00", "12:59:00", 1), "0", "1", "unavailable"},
		{"failed read cannot prove quiet", start, lifetime, end, "unavailable", "", "0", "0", "unavailable"},
		{"partial failed read retains errors", start, lifetime, end, "unavailable", historical, "0", "1", "unavailable"},
		{"invalid quiet boundary fails closed", "not-a-date", lifetime, end, "complete", "", "0", "0", "unavailable"},
		{"no local timezone conversion", "2026-09-12T14:30:00+00:00", lifetime, end, "complete", historical, "0", "1", "unavailable"},
		{"excess timestamp precision", start, lifetime, end, "complete", strings.Replace(boundary, "123456789Z", "1234567890Z", 1), "0", "1", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program := builderErrorWindowProgram(t, collector) + "\nsummarize_builder_error_window " +
				errorWindowQuote(tc.start) + " " + errorWindowQuote(tc.lifetime) + " " +
				errorWindowQuote(tc.end) + " " + errorWindowQuote(tc.status) + "\n"
			got := runPressureShell(t, program, tc.log)
			for key, want := range map[string]string{
				"builder_error_events_before_observation": tc.before,
				"builder_error_events_during_observation": tc.during,
				"builder_error_window_status":             tc.wantStatus,
			} {
				if got[key] != want {
					t.Errorf("%s=%q, want %q (sample %v)", key, got[key], want, got)
				}
			}
		})
	}
}

func TestBuilderErrorWindowCollectorKeepsLifecycleAndLogReadProvenance(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	start := strings.Index(collector, "builder_error_window_until=$(date")
	if start < 0 {
		t.Fatal("collector no longer captures a remote error-window end")
	}
	const passStart = "2026-09-12T14:30:00Z compatibility: builder pass start full=false since=2026-09-12T14:00:00Z"
	const passComplete = "2026-09-12T14:30:05Z compatibility: builder pass complete full=false since=2026-09-12T14:00:00Z"
	const historical = "2026-09-12T14:28:00Z compatibility: builder run failed: pre-existing error"
	for _, tc := range []struct {
		name, before, log, exitCode, active, state, total, window string
	}{
		{"completed pass with history", passComplete, historical + "\n" + passComplete, "0", "false", "complete", "1", "complete"},
		{"same active pass", passStart, passStart, "0", "true", "start", "0", "complete"},
		{"completion racing latency", passStart, passComplete, "0", "false", "race", "0", "complete"},
		{"failed read", passStart, "docker: log read failed", "1", "false", "race", "0", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program := builderErrorWindowProgram(t, collector) + "\n" +
				"builder_lifecycle_pattern=" + errorWindowQuote(collectorPattern(t, collector, "builder_lifecycle_pattern")) + "\n" +
				"observe_since=2026-09-12T13:00:00Z\nobservation_started_at=2026-09-12T14:30:00Z\ncontainer=fixture\n" +
				"builder_lifecycle_before=" + errorWindowQuote(tc.before) + "\n" +
				"date() { printf '2026-09-12T14:31:00.123456789Z\\n'; }\n" +
				"docker() {\n" +
				"  [ \"$1|$2|$3|$4|$5|$6|$7\" = 'logs|--since|2026-09-12T13:00:00Z|--until|2026-09-12T14:31:00.123456789Z|--timestamps|fixture' ] || return 2\n" +
				"  cat\n  return " + tc.exitCode + "\n}\n" + collector[start:]
			got := runPressureShell(t, program, tc.log)
			for key, want := range map[string]string{
				"builder_active":                tc.active,
				"builder_lifecycle_state":       tc.state,
				"builder_error_events":          tc.total,
				"builder_error_window_status":   tc.window,
				"builder_error_window_ended_at": "2026-09-12T14:31:00.123456789Z",
				"observed_at":                   "2026-09-12T14:31:00Z",
			} {
				if got[key] != want {
					t.Errorf("%s=%q, want %q (sample %v)", key, got[key], want, got)
				}
			}
		})
	}
}

func TestObservationPressureWindowPreservesLifetimeCountersWithoutInventingDeltas(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	const start = "2026-09-12T14:30:00.123456789Z"
	const prefix = "2026-09-12T14:30:00.123456789Z "
	const pressure = "csx-server: db pressure path=/wanted class=interactive cause=pool_busy+query_timeout " +
		"pool_busy=3 query_timeout=2 admission_refused=0 deferred_refused=0 waited=1m2.5s " +
		"pool_busy_total=57 query_timeout_total=18 admission_refused_total=96 deferred_refused_total=4"
	for _, tc := range []struct {
		name, log, readStatus, lines, busy, timeouts, wait, status string
	}{
		{"quiet", "", "complete", "0", "0", "0", "0.000000000", "complete"},
		{"historical pressure excluded", "2026-09-12T14:28:00Z " + pressure, "complete", "0", "0", "0", "0.000000000", "complete"},
		{"boundary counts new line not cumulative totals", prefix + pressure, "complete", "1", "1", "1", "62.500000000", "complete"},
		{"one nanosecond before excluded", strings.Replace(prefix, "123456789Z", "123456788Z", 1) + pressure, "complete", "0", "0", "0", "0.000000000", "complete"},
		{"zero pressure with nonzero lifetime totals", prefix + strings.NewReplacer("pool_busy=3", "pool_busy=0", "query_timeout=2", "query_timeout=0", "waited=1m2.5s", "waited=0s").Replace(pressure), "complete", "1", "0", "0", "0.000000000", "complete"},
		{"microsecond Go duration", prefix + strings.Replace(pressure, "1m2.5s", "2.5µs", 1), "complete", "1", "1", "1", "0.000002500", "complete"},
		{"nanosecond duration", prefix + strings.Replace(pressure, "1m2.5s", "1ns", 1), "complete", "1", "1", "1", "0.000000001", "complete"},
		{"invalid duration is unavailable", prefix + strings.Replace(pressure, "1m2.5s", "n/a", 1), "complete", "1", "1", "1", "0.000000000", "unavailable"},
		{"partly valid duration is unavailable", prefix + strings.Replace(pressure, "1m2.5s", "1s-invalid", 1), "complete", "1", "1", "1", "0.000000000", "unavailable"},
		{"missing duration is unavailable", prefix + strings.Replace(pressure, "waited=1m2.5s ", "", 1), "complete", "1", "1", "1", "0.000000000", "unavailable"},
		{"duplicate duration is unavailable", prefix + pressure + " waited=2s", "complete", "1", "1", "1", "62.500000000", "unavailable"},
		{"missing query count is unavailable", prefix + strings.Replace(pressure, "query_timeout=2 ", "", 1), "complete", "1", "1", "0", "62.500000000", "unavailable"},
		{"malformed timestamp is unavailable", "invalid " + pressure, "complete", "0", "0", "0", "0.000000000", "unavailable"},
		{"failed read cannot prove quiet", "", "unavailable", "0", "0", "0", "0.000000000", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program := builderErrorWindowProgram(t, collector) + "\nsummarize_builder_error_window " +
				errorWindowQuote(start) + " '2026-09-12T13:00:00Z' '2026-09-12T14:31:00Z' " + errorWindowQuote(tc.readStatus) + "\n"
			got := runPressureShell(t, program, tc.log)
			for key, want := range map[string]string{
				"window_pressure_lines":            tc.lines,
				"window_pool_busy_events":          tc.busy,
				"window_query_timeout_events":      tc.timeouts,
				"window_max_pressure_wait_seconds": tc.wait,
				"pressure_window_status":           tc.status,
			} {
				if got[key] != want {
					t.Errorf("%s=%q, want %q (sample %v)", key, got[key], want, got)
				}
			}
		})
	}
}

func builderErrorWindowProgram(t *testing.T, collector string) string {
	t.Helper()
	return "builder_error_pattern=" + errorWindowQuote(collectorPattern(t, collector, "builder_error_pattern")) + "\n" +
		shellFunction(t, collector, "summarize_builder_error_window")
}

func errorWindowQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
